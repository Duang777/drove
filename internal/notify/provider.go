package notify

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPClient sends provider requests and permits deterministic channel tests.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type providerRequestError struct {
	cause error
}

func (e *providerRequestError) Error() string {
	return "notify: provider request failed"
}

func (e *providerRequestError) Unwrap() error {
	return e.cause
}

func classifyHTTPResponse(response *http.Response, revokeMissing bool) SendResult {
	code := "http_" + strconv.Itoa(response.StatusCode)
	switch {
	case response.StatusCode >= http.StatusOK &&
		response.StatusCode < http.StatusMultipleChoices:
		return SendResult{Outcome: SendDelivered, Code: code}
	case revokeMissing &&
		(response.StatusCode == http.StatusNotFound ||
			response.StatusCode == http.StatusGone):
		return SendResult{Outcome: SendRevokeTarget, Code: code}
	case response.StatusCode == http.StatusRequestTimeout ||
		response.StatusCode == http.StatusTooEarly ||
		response.StatusCode == http.StatusTooManyRequests ||
		response.StatusCode >= http.StatusInternalServerError:
		return SendResult{
			Outcome:    SendRetry,
			RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
			Code:       code,
		}
	default:
		return SendResult{Outcome: SendPermanent, Code: code}
	}
}

func parseRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	seconds, err := strconv.ParseInt(raw, 10, 32)
	if err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	at, err := http.ParseTime(raw)
	if err != nil || !at.After(now) {
		return 0
	}
	return at.Sub(now)
}

func sanitizeProviderError(err error) error {
	if err == nil {
		return nil
	}
	return &providerRequestError{cause: err}
}
