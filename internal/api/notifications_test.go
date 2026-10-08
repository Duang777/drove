package api

import (
	"context"
	"crypto/elliptic"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/notify"
)

const testSubscriptionID = "550e8400-e29b-41d4-a716-446655440001"

func TestNotificationStatusAndListHidePushSecrets(t *testing.T) {
	server, _, _ := newTestServer(t)
	service := &fakeNotificationService{
		subscriptions: []notify.PushSubscription{
			{
				ID:         testSubscriptionID,
				Endpoint:   "https://push.example.test/SECRET-ENDPOINT",
				P256DH:     "SECRET-P256DH",
				Auth:       "SECRET-AUTH",
				DeviceName: "MacBook Chrome",
				CreatedAt:  time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC),
			},
			{
				ID:         "550e8400-e29b-41d4-a716-446655440002",
				Endpoint:   "https://push.example.test/revoked",
				P256DH:     "revoked-key",
				Auth:       "revoked-auth",
				DeviceName: "Old phone",
				CreatedAt:  time.Date(2026, time.October, 8, 11, 0, 0, 0, time.UTC),
				RevokedAt:  timePointer(time.Date(2026, time.October, 8, 13, 0, 0, 0, time.UTC)),
			},
		},
	}
	server.opts.Notifications = NotificationOptions{
		Service:          service,
		WebPushPublicKey: "public-vapid",
		NtfyEnabled:      true,
		Debounce:         30 * time.Second,
		QuietWhenActive:  true,
	}

	status := httptest.NewRecorder()
	serveAuthorized(
		server,
		status,
		httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil),
	)
	if status.Code != http.StatusOK {
		t.Fatalf("status response = %d %q", status.Code, status.Body.String())
	}
	wantStatus := `{"web_push":{"available":true,"vapid_public_key":"public-vapid"},` +
		`"ntfy":{"available":true},"policy":{"on":["blocked"],` +
		`"debounce_seconds":30,"quiet_when_active":true},"active_device_count":1}` + "\n"
	if status.Body.String() != wantStatus {
		t.Fatalf("status body = %s, want %s", status.Body.String(), wantStatus)
	}

	list := httptest.NewRecorder()
	serveAuthorized(
		server,
		list,
		httptest.NewRequest(http.MethodGet, "/api/v1/push/subscriptions", nil),
	)
	if list.Code != http.StatusOK {
		t.Fatalf("list response = %d %q", list.Code, list.Body.String())
	}
	body := list.Body.String()
	if !strings.Contains(body, testSubscriptionID) ||
		!strings.Contains(body, "MacBook Chrome") ||
		strings.Contains(body, "SECRET") ||
		strings.Contains(body, "Old phone") ||
		strings.Contains(body, "endpoint") ||
		strings.Contains(body, "p256dh") ||
		strings.Contains(body, "auth") {
		t.Fatalf("unsafe subscription response: %s", body)
	}
}

func TestSavePushSubscriptionStrictlyValidatesRequest(t *testing.T) {
	publicKey := elliptic.Marshal(
		elliptic.P256(),
		elliptic.P256().Params().Gx,
		elliptic.P256().Params().Gy,
	)
	p256dh := base64.RawURLEncoding.EncodeToString(publicKey)
	auth := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
	validBody := `{"endpoint":"https://push.example.test/device?token=one",` +
		`"keys":{"p256dh":"` + p256dh + `","auth":"` + auth + `"},` +
		`"device_name":"MacBook Chrome"}`

	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
	}{
		{
			name:        "valid",
			contentType: "application/json",
			body:        validBody,
			wantStatus:  http.StatusCreated,
		},
		{
			name:        "wrong content type",
			contentType: "text/plain",
			body:        validBody,
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "unknown field",
			contentType: "application/json",
			body: strings.Replace(
				validBody,
				`"auth":"`+auth+`"`,
				`"auth":"`+auth+`","extra":true`,
				1,
			),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "HTTP endpoint",
			contentType: "application/json",
			body: strings.Replace(
				validBody,
				"https://push.example.test",
				"http://push.example.test",
				1,
			),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "padded key",
			contentType: "application/json",
			body: strings.Replace(
				validBody,
				`"p256dh":"`+p256dh+`"`,
				`"p256dh":"`+p256dh+`="`,
				1,
			),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "control device name",
			contentType: "application/json",
			body: strings.Replace(
				validBody,
				"MacBook Chrome",
				`MacBook\u0000Chrome`,
				1,
			),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "trailing object",
			contentType: "application/json",
			body:        validBody + `{}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "oversized body",
			contentType: "application/json",
			body:        strings.Repeat(" ", maxPushSubscriptionRequestBytes+1),
			wantStatus:  http.StatusRequestEntityTooLarge,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, _ := newTestServer(t)
			service := &fakeNotificationService{}
			server.opts.Notifications = NotificationOptions{
				Service:          service,
				WebPushPublicKey: "public-vapid",
			}
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/push/subscriptions",
				strings.NewReader(test.body),
			)
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			serveAuthorized(server, response, request)
			if response.Code != test.wantStatus {
				t.Fatalf(
					"response = %d %q, want %d",
					response.Code,
					response.Body.String(),
					test.wantStatus,
				)
			}
			if test.wantStatus == http.StatusCreated {
				if len(service.saved) != 1 ||
					service.saved[0].Endpoint != "https://push.example.test/device?token=one" ||
					service.saved[0].DeviceName != "MacBook Chrome" {
					t.Fatalf("saved subscriptions = %+v", service.saved)
				}
				body := response.Body.String()
				if strings.Contains(body, "endpoint") ||
					strings.Contains(body, "p256dh") ||
					strings.Contains(body, "auth") {
					t.Fatalf("save response exposed push secrets: %s", body)
				}
			}
		})
	}
}

func TestNotificationMutationsUseCurrentTargetAndAuthentication(t *testing.T) {
	server, _, _ := newTestServer(t)
	service := &fakeNotificationService{
		subscriptions: []notify.PushSubscription{{
			ID:         testSubscriptionID,
			DeviceName: "Phone",
			CreatedAt:  time.Now().UTC(),
		}},
		revokeResult: true,
	}
	server.opts.Notifications = NotificationOptions{
		Service:          service,
		WebPushPublicKey: "public-vapid",
	}

	unauthorized := httptest.NewRecorder()
	server.mux.ServeHTTP(
		unauthorized,
		httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil),
	)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorized.Code)
	}

	presence := httptest.NewRecorder()
	serveAuthorized(
		server,
		presence,
		httptest.NewRequest(
			http.MethodPost,
			"/api/v1/notifications/presence",
			nil,
		),
	)
	if presence.Code != http.StatusNoContent || service.presenceCalls != 1 {
		t.Fatalf(
			"presence = %d calls=%d",
			presence.Code,
			service.presenceCalls,
		)
	}

	testResponse := httptest.NewRecorder()
	testRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/notifications/test",
		strings.NewReader(`{"subscription_id":"`+testSubscriptionID+`"}`),
	)
	testRequest.Header.Set("Content-Type", "application/json")
	serveAuthorized(server, testResponse, testRequest)
	if testResponse.Code != http.StatusNoContent ||
		service.testedID != testSubscriptionID {
		t.Fatalf(
			"test response = %d tested=%q body=%q",
			testResponse.Code,
			service.testedID,
			testResponse.Body.String(),
		)
	}

	revoke := httptest.NewRecorder()
	serveAuthorized(
		server,
		revoke,
		httptest.NewRequest(
			http.MethodDelete,
			"/api/v1/push/subscriptions/"+testSubscriptionID,
			nil,
		),
	)
	if revoke.Code != http.StatusNoContent ||
		service.revokedID != testSubscriptionID {
		t.Fatalf(
			"revoke response = %d revoked=%q",
			revoke.Code,
			service.revokedID,
		)
	}
}

func TestNotificationCookieWriteRequiresExactHTTPSOrigin(t *testing.T) {
	server, _, _ := newTestServer(t)
	service := &fakeNotificationService{}
	server.opts.Notifications.Service = service
	server.allowedOrigins["https://drove.example.ts.net"] = struct{}{}
	code, err := server.opts.Auth.IssueLoginCode()
	if err != nil {
		t.Fatalf("issue login code: %v", err)
	}
	cookieValue, _, err := server.opts.Auth.ExchangeLoginCode(code)
	if err != nil {
		t.Fatalf("exchange login code: %v", err)
	}
	handler := server.Handler(BrowserAccess, "drove.example.ts.net")

	for _, test := range []struct {
		name       string
		origin     string
		wantStatus int
	}{
		{name: "missing", wantStatus: http.StatusForbidden},
		{name: "HTTP near match", origin: "http://drove.example.ts.net", wantStatus: http.StatusForbidden},
		{name: "exact HTTPS", origin: "https://drove.example.ts.net", wantStatus: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/notifications/presence",
				nil,
			)
			request.Host = "drove.example.ts.net"
			request.AddCookie(&http.Cookie{
				Name:  sessionCookieName,
				Value: cookieValue,
			})
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf(
					"response = %d %q, want %d",
					response.Code,
					response.Body.String(),
					test.wantStatus,
				)
			}
		})
	}
}

type fakeNotificationService struct {
	subscriptions []notify.PushSubscription
	saved         []notify.PushSubscription
	revokeResult  bool
	revokeErr     error
	revokedID     string
	presenceCalls int
	testedID      string
	testErr       error
}

func (f *fakeNotificationService) SavePushSubscription(
	_ context.Context,
	subscription notify.PushSubscription,
) (notify.PushSubscription, error) {
	f.saved = append(f.saved, subscription)
	return subscription, nil
}

func (f *fakeNotificationService) PushSubscriptions(
	context.Context,
) ([]notify.PushSubscription, error) {
	return append([]notify.PushSubscription(nil), f.subscriptions...), nil
}

func (f *fakeNotificationService) RevokePushSubscription(
	_ context.Context,
	id string,
) (bool, error) {
	f.revokedID = id
	return f.revokeResult, f.revokeErr
}

func (f *fakeNotificationService) RecordPresence() {
	f.presenceCalls++
}

func (f *fakeNotificationService) SendPushTest(
	_ context.Context,
	id string,
) error {
	f.testedID = id
	return f.testErr
}

func timePointer(value time.Time) *time.Time {
	return &value
}

var _ NotificationService = (*fakeNotificationService)(nil)
