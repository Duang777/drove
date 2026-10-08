package notify

import (
	"context"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func TestWebPushChannelSendsEncryptedPayloadWithStableTopic(t *testing.T) {
	credentials := testVAPIDCredentials(t)
	var requests []*http.Request
	client := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read encrypted request: %v", err)
		}
		if strings.Contains(string(body), "Build agent") {
			t.Fatal("Web Push request contains an unencrypted notification")
		}
		return providerResponse(http.StatusCreated, ""), nil
	})
	channel, err := NewWebPushChannel(WebPushOptions{
		Subject:     "mailto:operator@example.com",
		Credentials: credentials,
		HTTPClient:  client,
	})
	if err != nil {
		t.Fatalf("create Web Push channel: %v", err)
	}
	delivery := testChannelDelivery(t)

	first, err := channel.Send(context.Background(), delivery)
	if err != nil {
		t.Fatalf("send first Web Push: %v", err)
	}
	second, err := channel.Send(context.Background(), delivery)
	if err != nil {
		t.Fatalf("send second Web Push: %v", err)
	}
	if first.Outcome != SendDelivered || second.Outcome != SendDelivered {
		t.Fatalf("send outcomes = %q, %q", first.Outcome, second.Outcome)
	}
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	firstTopic := requests[0].Header.Get("Topic")
	if len(firstTopic) != 32 || requests[1].Header.Get("Topic") != firstTopic {
		t.Fatalf(
			"Web Push topics = %q, %q; want one stable 32-byte value",
			firstTopic,
			requests[1].Header.Get("Topic"),
		)
	}
	if firstTopic == webPushTopic("another-delivery") {
		t.Fatal("different delivery IDs have the same Web Push topic")
	}
	if requests[0].Header.Get("TTL") != "300" ||
		requests[0].Header.Get("Urgency") != "high" ||
		!strings.HasPrefix(requests[0].Header.Get("Authorization"), "vapid ") {
		t.Fatalf("Web Push headers = %#v", requests[0].Header)
	}
	if channel.PublicKey() != credentials.PublicKey {
		t.Fatal("channel returned the wrong public VAPID key")
	}
}

func TestWebPushChannelClassifiesProviderResponses(t *testing.T) {
	tests := []struct {
		status  int
		outcome SendOutcome
	}{
		{status: http.StatusOK, outcome: SendDelivered},
		{status: http.StatusNotFound, outcome: SendRevokeTarget},
		{status: http.StatusGone, outcome: SendRevokeTarget},
		{status: http.StatusRequestTimeout, outcome: SendRetry},
		{status: http.StatusTooEarly, outcome: SendRetry},
		{status: http.StatusTooManyRequests, outcome: SendRetry},
		{status: http.StatusInternalServerError, outcome: SendRetry},
		{status: http.StatusBadGateway, outcome: SendRetry},
		{status: http.StatusBadRequest, outcome: SendPermanent},
		{status: http.StatusUnauthorized, outcome: SendPermanent},
	}
	for _, test := range tests {
		t.Run(strconv.Itoa(test.status), func(t *testing.T) {
			channel, err := NewWebPushChannel(WebPushOptions{
				Subject:     "https://drove.example.com/contact",
				Credentials: testVAPIDCredentials(t),
				HTTPClient: httpClientFunc(
					func(*http.Request) (*http.Response, error) {
						response := providerResponse(test.status, "")
						if test.status == http.StatusTooManyRequests {
							response.Header.Set("Retry-After", "17")
						}
						return response, nil
					},
				),
			})
			if err != nil {
				t.Fatalf("create Web Push channel: %v", err)
			}
			result, err := channel.Send(context.Background(), testChannelDelivery(t))
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if result.Outcome != test.outcome {
				t.Fatalf("outcome = %q, want %q", result.Outcome, test.outcome)
			}
			if test.status == http.StatusTooManyRequests &&
				result.RetryAfter != 17*time.Second {
				t.Fatalf("retry delay = %v, want 17s", result.RetryAfter)
			}
		})
	}
}

func TestWebPushChannelSanitizesNetworkErrors(t *testing.T) {
	sentinel := errors.New("SECRET-ENDPOINT")
	channel, err := NewWebPushChannel(WebPushOptions{
		Subject:     "mailto:operator@example.com",
		Credentials: testVAPIDCredentials(t),
		HTTPClient: httpClientFunc(func(*http.Request) (*http.Response, error) {
			return nil, sentinel
		}),
	})
	if err != nil {
		t.Fatalf("create Web Push channel: %v", err)
	}
	_, err = channel.Send(context.Background(), testChannelDelivery(t))
	if err == nil {
		t.Fatal("send succeeded")
	}
	if strings.Contains(err.Error(), "SECRET-ENDPOINT") {
		t.Fatalf("sanitized error leaked endpoint: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("sanitized error does not wrap cause: %v", err)
	}
}

func TestWebPushChannelDeadLettersInvalidSubscription(t *testing.T) {
	called := false
	channel, err := NewWebPushChannel(WebPushOptions{
		Subject:     "mailto:operator@example.com",
		Credentials: testVAPIDCredentials(t),
		HTTPClient: httpClientFunc(func(*http.Request) (*http.Response, error) {
			called = true
			return providerResponse(http.StatusCreated, ""), nil
		}),
	})
	if err != nil {
		t.Fatalf("create Web Push channel: %v", err)
	}
	delivery := testChannelDelivery(t)
	delivery.Subscription.P256DH = "not-a-key"
	result, err := channel.Send(context.Background(), delivery)
	if err != nil {
		t.Fatalf("send invalid subscription: %v", err)
	}
	if result.Outcome != SendPermanent || result.Code != "invalid_subscription" {
		t.Fatalf("result = %+v", result)
	}
	if called {
		t.Fatal("provider was called for an invalid subscription")
	}
}

func TestNtfyChannelPostsMetadataAndBearerToken(t *testing.T) {
	const token = "tk_SECRET_TOKEN"
	var gotPath string
	var gotAuthorization string
	var gotBody string
	var bodyErr error
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		gotPath = request.URL.Path
		gotAuthorization = request.Header.Get("Authorization")
		var raw []byte
		raw, bodyErr = io.ReadAll(request.Body)
		gotBody = string(raw)
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	channel, err := NewNtfyChannel(NtfyOptions{
		BaseURL:    server.URL + "/root/",
		Topic:      "drove_ops",
		Token:      token,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("create ntfy channel: %v", err)
	}
	result, err := channel.Send(context.Background(), testChannelDelivery(t))
	if err != nil {
		t.Fatalf("send ntfy notification: %v", err)
	}
	if result.Outcome != SendDelivered {
		t.Fatalf("outcome = %q, want delivered", result.Outcome)
	}
	if bodyErr != nil {
		t.Fatalf("read ntfy request: %v", bodyErr)
	}
	if gotPath != "/root/drove_ops" {
		t.Fatalf("ntfy path = %q", gotPath)
	}
	if gotAuthorization != "Bearer "+token {
		t.Fatalf("ntfy authorization = %q", gotAuthorization)
	}
	for _, value := range []string{
		"Build agent",
		"generic",
		"blocked",
		"42",
		"/?agent=agent-1",
	} {
		if !strings.Contains(gotBody, value) {
			t.Fatalf("ntfy body %q does not contain %q", gotBody, value)
		}
	}
	if strings.Contains(gotBody, token) {
		t.Fatal("ntfy body contains bearer token")
	}
}

func TestNtfyChannelRetriesTransientFailureWithoutLeakingToken(t *testing.T) {
	const secret = "SECRET-NTFY-TOKEN"
	sentinel := errors.New(secret)
	channel, err := NewNtfyChannel(NtfyOptions{
		BaseURL: "https://ntfy.example.com",
		Topic:   "drove",
		Token:   secret,
		HTTPClient: httpClientFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("Authorization") != "Bearer "+secret {
				t.Fatal("missing ntfy bearer token")
			}
			return nil, sentinel
		}),
	})
	if err != nil {
		t.Fatalf("create ntfy channel: %v", err)
	}
	_, err = channel.Send(context.Background(), testChannelDelivery(t))
	if err == nil {
		t.Fatal("send succeeded")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("sanitized error leaked token: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("sanitized error does not wrap cause: %v", err)
	}
}

func TestNtfyChannelClassifiesHTTPFailures(t *testing.T) {
	for _, test := range []struct {
		status  int
		outcome SendOutcome
	}{
		{status: http.StatusRequestTimeout, outcome: SendRetry},
		{status: http.StatusTooEarly, outcome: SendRetry},
		{status: http.StatusTooManyRequests, outcome: SendRetry},
		{status: http.StatusServiceUnavailable, outcome: SendRetry},
		{status: http.StatusNotFound, outcome: SendPermanent},
		{status: http.StatusForbidden, outcome: SendPermanent},
	} {
		t.Run(strconv.Itoa(test.status), func(t *testing.T) {
			channel, err := NewNtfyChannel(NtfyOptions{
				BaseURL: "https://ntfy.example.com",
				Topic:   "drove",
				HTTPClient: httpClientFunc(
					func(*http.Request) (*http.Response, error) {
						return providerResponse(test.status, ""), nil
					},
				),
			})
			if err != nil {
				t.Fatalf("create ntfy channel: %v", err)
			}
			result, err := channel.Send(context.Background(), testChannelDelivery(t))
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if result.Outcome != test.outcome {
				t.Fatalf("outcome = %q, want %q", result.Outcome, test.outcome)
			}
		})
	}
}

func TestLoadNtfyTokenRequiresPrivateRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ntfy.token")
	if err := os.WriteFile(path, []byte("tk_valid\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	token, err := LoadNtfyToken(path)
	if err != nil {
		t.Fatalf("load token: %v", err)
	}
	if token != "tk_valid" {
		t.Fatalf("token = %q, want tk_valid", token)
	}

	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("set token mode: %v", err)
	}
	if _, err := LoadNtfyToken(path); err == nil ||
		!strings.Contains(err.Error(), "want 0600") {
		t.Fatalf("open-mode token error = %v", err)
	}

	target := filepath.Join(t.TempDir(), "target.token")
	if err := os.WriteFile(target, []byte("tk_target"), 0o600); err != nil {
		t.Fatalf("write token target: %v", err)
	}
	link := filepath.Join(t.TempDir(), "token.link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink token: %v", err)
	}
	if _, err := LoadNtfyToken(link); err == nil ||
		!strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink token error = %v", err)
	}
}

func TestLoadNtfyTokenRejectsEmptyAndMultilineValues(t *testing.T) {
	for _, raw := range []string{"", "\n", "token\nextra\n", " token", "token\tvalue"} {
		t.Run(strconv.Quote(raw), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ntfy.token")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatalf("write token: %v", err)
			}
			if _, err := LoadNtfyToken(path); err == nil {
				t.Fatalf("loaded invalid token %q", raw)
			}
		})
	}
}

func TestNtfyEndpointRequiresHTTPSOrLoopbackHTTP(t *testing.T) {
	for _, raw := range []string{
		"http://ntfy.example.com",
		"ftp://ntfy.example.com",
		"https://token@ntfy.example.com",
		"https://ntfy.example.com?token=secret",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := NewNtfyChannel(NtfyOptions{
				BaseURL: raw,
				Topic:   "drove",
			}); err == nil {
				t.Fatalf("accepted ntfy base URL %q", raw)
			}
		})
	}
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (f httpClientFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func providerResponse(status int, retryAfter string) *http.Response {
	header := make(http.Header)
	if retryAfter != "" {
		header.Set("Retry-After", retryAfter)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader("provider response")),
	}
}

func testVAPIDCredentials(t *testing.T) VAPIDCredentials {
	t.Helper()
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("generate VAPID credentials: %v", err)
	}
	return VAPIDCredentials{
		PublicKey:  publicKey,
		PrivateKey: privateKey,
	}
}

func testChannelDelivery(t *testing.T) Delivery {
	t.Helper()
	privateKey, x, y, err := elliptic.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate subscription key: %v", err)
	}
	_ = privateKey
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("generate subscription auth: %v", err)
	}
	return Delivery{
		ID:        "delivery-1",
		SourceSeq: 42,
		AgentID:   "agent-1",
		Channel:   ChannelWebPush,
		TargetID:  "phone",
		Notification: Notification{
			ID:         "notification-42",
			AgentID:    "agent-1",
			AgentName:  "Build agent",
			Vendor:     "generic",
			State:      "blocked",
			BlockedSeq: 42,
			OccurredAt: time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC),
			DeepLink:   "/?agent=agent-1",
		},
		Subscription: &PushSubscription{
			ID:         "phone",
			Endpoint:   "https://push.example.com/send/SECRET-ENDPOINT",
			P256DH:     base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), x, y)),
			Auth:       base64.RawURLEncoding.EncodeToString(auth),
			DeviceName: "Phone",
			CreatedAt:  time.Date(2026, time.October, 8, 11, 0, 0, 0, time.UTC),
		},
	}
}
