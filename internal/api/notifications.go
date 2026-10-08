package api

import (
	"bytes"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/notify"
)

const (
	maxPushSubscriptionRequestBytes = 16 * 1024
	maxNotificationTestRequestBytes = 1024
	maxPushEndpointBytes            = 4096
	maxPushKeyBytes                 = 256
	maxDeviceNameRunes              = 80
)

type notificationStatusResponse struct {
	WebPush           webPushStatusResponse `json:"web_push"`
	Ntfy              channelStatusResponse `json:"ntfy"`
	Policy            notificationPolicy    `json:"policy"`
	ActiveDeviceCount int                   `json:"active_device_count"`
}

type webPushStatusResponse struct {
	Available      bool   `json:"available"`
	VAPIDPublicKey string `json:"vapid_public_key,omitempty"`
}

type channelStatusResponse struct {
	Available bool `json:"available"`
}

type notificationPolicy struct {
	On              []string `json:"on"`
	DebounceSeconds int64    `json:"debounce_seconds"`
	QuietWhenActive bool     `json:"quiet_when_active"`
}

type pushSubscriptionResponse struct {
	ID         string    `json:"id"`
	DeviceName string    `json:"device_name"`
	CreatedAt  time.Time `json:"created_at"`
}

type pushSubscriptionRequest struct {
	Endpoint   string                      `json:"endpoint"`
	Keys       pushSubscriptionKeysRequest `json:"keys"`
	DeviceName string                      `json:"device_name"`
}

type pushSubscriptionKeysRequest struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type notificationTestRequest struct {
	SubscriptionID string `json:"subscription_id"`
}

func (s *Server) handleNotificationStatus(w http.ResponseWriter, r *http.Request) {
	if !rejectNotificationQuery(w, r) {
		return
	}
	activeDevices := 0
	if s.opts.Notifications.Service != nil {
		subscriptions, err := s.opts.Notifications.Service.PushSubscriptions(
			r.Context(),
		)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read notification status")
			return
		}
		for _, subscription := range subscriptions {
			if subscription.RevokedAt == nil {
				activeDevices++
			}
		}
	}
	webPushAvailable := s.webPushAvailable()
	publicKey := ""
	if webPushAvailable {
		publicKey = s.opts.Notifications.WebPushPublicKey
	}
	writeJSON(w, http.StatusOK, notificationStatusResponse{
		WebPush: webPushStatusResponse{
			Available:      webPushAvailable,
			VAPIDPublicKey: publicKey,
		},
		Ntfy: channelStatusResponse{
			Available: s.opts.Notifications.Service != nil &&
				s.opts.Notifications.NtfyEnabled,
		},
		Policy: notificationPolicy{
			On:              []string{"blocked"},
			DebounceSeconds: int64(s.opts.Notifications.Debounce / time.Second),
			QuietWhenActive: s.opts.Notifications.QuietWhenActive,
		},
		ActiveDeviceCount: activeDevices,
	})
}

func (s *Server) handlePushSubscriptions(w http.ResponseWriter, r *http.Request) {
	if !rejectNotificationQuery(w, r) {
		return
	}
	if s.opts.Notifications.Service == nil {
		writeJSON(w, http.StatusOK, []pushSubscriptionResponse{})
		return
	}
	subscriptions, err := s.opts.Notifications.Service.PushSubscriptions(
		r.Context(),
	)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list push subscriptions")
		return
	}
	response := make([]pushSubscriptionResponse, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		if subscription.RevokedAt != nil {
			continue
		}
		response = append(response, publicPushSubscription(subscription))
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSavePushSubscription(w http.ResponseWriter, r *http.Request) {
	if !rejectNotificationQuery(w, r) {
		return
	}
	if !s.webPushAvailable() {
		writeErr(w, http.StatusConflict, "Web Push is not enabled")
		return
	}
	var request pushSubscriptionRequest
	if !decodeNotificationJSON(
		w,
		r,
		maxPushSubscriptionRequestBytes,
		&request,
	) {
		return
	}
	if err := validatePushSubscriptionRequest(request); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := s.opts.Notifications.Service.SavePushSubscription(
		r.Context(),
		notify.PushSubscription{
			ID:         uuid.NewString(),
			Endpoint:   request.Endpoint,
			P256DH:     request.Keys.P256DH,
			Auth:       request.Keys.Auth,
			DeviceName: request.DeviceName,
			CreatedAt:  time.Now().UTC(),
		},
	)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save push subscription")
		return
	}
	writeJSON(w, http.StatusCreated, publicPushSubscription(saved))
}

func (s *Server) handleRevokePushSubscription(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !rejectNotificationQuery(w, r) {
		return
	}
	if s.opts.Notifications.Service == nil {
		writeErr(w, http.StatusNotFound, "push subscription not found")
		return
	}
	id, err := canonicalUUID(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	revoked, err := s.opts.Notifications.Service.RevokePushSubscription(
		r.Context(),
		id,
	)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "revoke push subscription")
		return
	}
	if !revoked {
		writeErr(w, http.StatusNotFound, "push subscription not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNotificationPresence(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !rejectNotificationQuery(w, r) || !requireEmptyBody(w, r) {
		return
	}
	if s.opts.Notifications.Service != nil {
		s.opts.Notifications.Service.RecordPresence()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNotificationTest(w http.ResponseWriter, r *http.Request) {
	if !rejectNotificationQuery(w, r) {
		return
	}
	if !s.webPushAvailable() {
		writeErr(w, http.StatusConflict, "Web Push is not enabled")
		return
	}
	var request notificationTestRequest
	if !decodeNotificationJSON(
		w,
		r,
		maxNotificationTestRequestBytes,
		&request,
	) {
		return
	}
	id, err := canonicalUUID(request.SubscriptionID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.opts.Notifications.Service.SendPushTest(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, notify.ErrPushSubscriptionNotFound):
			writeErr(w, http.StatusNotFound, "push subscription not found")
		case errors.Is(err, notify.ErrPushSubscriptionRevoked):
			writeErr(w, http.StatusGone, "push subscription was revoked")
		case errors.Is(err, notify.ErrWebPushUnavailable):
			writeErr(w, http.StatusConflict, "Web Push is not enabled")
		default:
			writeErr(w, http.StatusBadGateway, "push provider rejected test notification")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) webPushAvailable() bool {
	return s.opts.Notifications.Service != nil &&
		s.opts.Notifications.WebPushPublicKey != ""
}

func publicPushSubscription(
	subscription notify.PushSubscription,
) pushSubscriptionResponse {
	return pushSubscriptionResponse{
		ID:         subscription.ID,
		DeviceName: subscription.DeviceName,
		CreatedAt:  subscription.CreatedAt,
	}
}

func validatePushSubscriptionRequest(request pushSubscriptionRequest) error {
	if request.Endpoint == "" ||
		request.Endpoint != strings.TrimSpace(request.Endpoint) ||
		len(request.Endpoint) > maxPushEndpointBytes {
		return errors.New("push endpoint must be 1-4096 bytes")
	}
	endpoint, err := url.Parse(request.Endpoint)
	if err != nil ||
		endpoint.Scheme != "https" ||
		endpoint.Host == "" ||
		endpoint.User != nil ||
		endpoint.Opaque != "" ||
		endpoint.Fragment != "" {
		return errors.New("push endpoint must be an HTTPS URL")
	}
	publicKey, err := decodeBase64URLKey(
		request.Keys.P256DH,
		"push p256dh key",
	)
	if err != nil {
		return err
	}
	if len(publicKey) != 65 {
		return errors.New("push p256dh key must decode to 65 bytes")
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), publicKey)
	if x == nil || y == nil {
		return errors.New("push p256dh key is not a P-256 public key")
	}
	auth, err := decodeBase64URLKey(request.Keys.Auth, "push auth key")
	if err != nil {
		return err
	}
	if len(auth) != 16 {
		return errors.New("push auth key must decode to 16 bytes")
	}
	if request.DeviceName == "" ||
		request.DeviceName != strings.TrimSpace(request.DeviceName) ||
		utf8.RuneCountInString(request.DeviceName) > maxDeviceNameRunes {
		return errors.New("device name must be 1-80 characters")
	}
	for _, char := range request.DeviceName {
		if unicode.IsControl(char) {
			return errors.New("device name must not contain control characters")
		}
	}
	return nil
}

func decodeBase64URLKey(raw string, name string) ([]byte, error) {
	if raw == "" || len(raw) > maxPushKeyBytes {
		return nil, errors.New(name + " must be a bounded Base64URL string")
	}
	for _, char := range raw {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			char == '-' ||
			char == '_' {
			continue
		}
		return nil, errors.New(name + " must use unpadded Base64URL")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New(name + " must use unpadded Base64URL")
	}
	return decoded, nil
}

func decodeNotificationJSON(
	w http.ResponseWriter,
	r *http.Request,
	maxBytes int64,
	target any,
) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeErr(
			w,
			http.StatusUnsupportedMediaType,
			"content type must be application/json",
		)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body exceeds maximum size")
			return false
		}
		writeErr(w, http.StatusBadRequest, "read request body")
		return false
	}
	if !utf8.Valid(body) {
		writeErr(w, http.StatusBadRequest, "request body must be valid UTF-8")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "request body must contain one JSON object")
		return false
	}
	return true
}

func requireEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(body) != 0 {
		writeErr(w, http.StatusBadRequest, "request body must be empty")
		return false
	}
	return true
}

func rejectNotificationQuery(w http.ResponseWriter, r *http.Request) bool {
	if len(r.URL.Query()) != 0 {
		writeErr(w, http.StatusBadRequest, "query parameters are not supported")
		return false
	}
	return true
}

func canonicalUUID(raw string) (string, error) {
	return canonicalUUIDField(raw, "subscription_id")
}

func canonicalUUIDField(raw string, field string) (string, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.String() != raw {
		return "", errors.New(field + " must be a canonical UUID")
	}
	return raw, nil
}

var _ NotificationService = (*notify.Service)(nil)
