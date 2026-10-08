package notify

import (
	"context"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

const defaultWebPushTTL = 5 * time.Minute

// WebPushOptions configures browser Push API delivery.
type WebPushOptions struct {
	Subject     string
	Credentials VAPIDCredentials
	HTTPClient  HTTPClient
}

// WebPushChannel delivers encrypted notifications to browser subscriptions.
type WebPushChannel struct {
	subject     string
	credentials VAPIDCredentials
	client      HTTPClient
}

// NewWebPushChannel validates options and constructs a Web Push channel.
func NewWebPushChannel(options WebPushOptions) (*WebPushChannel, error) {
	subject, err := webPushSubscriber(options.Subject)
	if err != nil {
		return nil, err
	}
	if err := options.Credentials.validate(); err != nil {
		return nil, err
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &WebPushChannel{
		subject:     subject,
		credentials: options.Credentials,
		client:      client,
	}, nil
}

// Kind implements Channel.
func (*WebPushChannel) Kind() ChannelKind {
	return ChannelWebPush
}

// PublicKey returns the browser-facing VAPID public key.
func (c *WebPushChannel) PublicKey() string {
	return c.credentials.PublicKey
}

// Send implements Channel.
func (c *WebPushChannel) Send(
	ctx context.Context,
	delivery Delivery,
) (SendResult, error) {
	if delivery.Subscription == nil {
		return SendResult{
			Outcome: SendPermanent,
			Code:    "missing_subscription",
		}, nil
	}
	if err := validateWebPushTarget(*delivery.Subscription); err != nil {
		return SendResult{
			Outcome: SendPermanent,
			Code:    "invalid_subscription",
		}, nil
	}
	payload, err := json.Marshal(delivery.Notification)
	if err != nil {
		return SendResult{
			Outcome: SendPermanent,
			Code:    "invalid_payload",
		}, nil
	}

	response, err := webpush.SendNotificationWithContext(
		ctx,
		payload,
		&webpush.Subscription{
			Endpoint: delivery.Subscription.Endpoint,
			Keys: webpush.Keys{
				P256dh: delivery.Subscription.P256DH,
				Auth:   delivery.Subscription.Auth,
			},
		},
		&webpush.Options{
			HTTPClient:      c.client,
			Subscriber:      c.subject,
			Topic:           webPushTopic(delivery.ID),
			TTL:             int(defaultWebPushTTL.Seconds()),
			Urgency:         webpush.UrgencyHigh,
			VAPIDPublicKey:  c.credentials.PublicKey,
			VAPIDPrivateKey: c.credentials.PrivateKey,
		},
	)
	if err != nil {
		if errors.Is(err, webpush.ErrMaxPadExceeded) {
			return SendResult{
				Outcome: SendPermanent,
				Code:    "payload_too_large",
			}, nil
		}
		return SendResult{}, sanitizeProviderError(err)
	}
	if response == nil {
		return SendResult{}, sanitizeProviderError(
			errors.New("provider returned no response"),
		)
	}
	if response.Body != nil {
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4*1024))
	}
	return classifyHTTPResponse(response, true), nil
}

func webPushSubscriber(subject string) (string, error) {
	parsed, err := url.Parse(subject)
	if err != nil {
		return "", errors.New("notify: Web Push subject is invalid")
	}
	switch parsed.Scheme {
	case "https":
		if parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return "", errors.New("notify: Web Push subject is invalid")
		}
		return subject, nil
	case "mailto":
		if parsed.Opaque == "" ||
			parsed.Host != "" ||
			parsed.User != nil ||
			parsed.RawQuery != "" ||
			parsed.Fragment != "" {
			return "", errors.New("notify: Web Push subject is invalid")
		}
		// webpush-go adds the mailto scheme for non-HTTPS subscribers.
		return parsed.Opaque, nil
	default:
		return "", errors.New("notify: Web Push subject is invalid")
	}
}

func validateWebPushTarget(subscription PushSubscription) error {
	endpoint, err := url.Parse(subscription.Endpoint)
	if err != nil ||
		endpoint.Scheme != "https" ||
		endpoint.Host == "" ||
		endpoint.User != nil ||
		endpoint.Fragment != "" {
		return errors.New("notify: invalid Web Push endpoint")
	}
	publicKey, err := decodePushKey(subscription.P256DH)
	if err != nil || len(publicKey) != 65 {
		return errors.New("notify: invalid Web Push public key")
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), publicKey)
	if x == nil || y == nil {
		return errors.New("notify: invalid Web Push public key")
	}
	auth, err := decodePushKey(subscription.Auth)
	if err != nil || len(auth) != 16 {
		return errors.New("notify: invalid Web Push auth secret")
	}
	return nil
}

func decodePushKey(raw string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(raw, "="))
	if err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(raw)
}

func webPushTopic(deliveryID string) string {
	digest := sha256.Sum256([]byte(deliveryID))
	return base64.RawURLEncoding.EncodeToString(digest[:24])
}
