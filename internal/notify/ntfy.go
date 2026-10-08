package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"
)

const maxNtfyTokenBytes = 4 * 1024

// NtfyOptions configures delivery to one ntfy topic.
type NtfyOptions struct {
	BaseURL    string
	Topic      string
	Token      string
	HTTPClient HTTPClient
}

// NtfyChannel delivers plain metadata notifications to ntfy.
type NtfyChannel struct {
	endpoint string
	token    string
	client   HTTPClient
}

// LoadNtfyToken reads an optional bearer token from a private regular file.
func LoadNtfyToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("notify: inspect ntfy token file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("notify: ntfy token file %q must be a regular file", path)
	}
	if info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf(
			"notify: ntfy token file %q permissions are %04o, want 0600",
			path,
			info.Mode().Perm(),
		)
	}
	if info.Size() < 1 || info.Size() > maxNtfyTokenBytes {
		return "", fmt.Errorf(
			"notify: ntfy token file %q must contain 1-%d bytes",
			path,
			maxNtfyTokenBytes,
		)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("notify: read ntfy token file %q: %w", path, err)
	}
	token := strings.TrimSuffix(string(raw), "\n")
	token = strings.TrimSuffix(token, "\r")
	if token == "" ||
		strings.TrimSpace(token) != token ||
		strings.IndexFunc(token, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("notify: ntfy token file %q contains an invalid token", path)
	}
	return token, nil
}

// NewNtfyChannel validates options and constructs an ntfy channel.
func NewNtfyChannel(options NtfyOptions) (*NtfyChannel, error) {
	endpoint, err := ntfyEndpoint(options.BaseURL, options.Topic)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(options.Token) != options.Token ||
		strings.IndexFunc(options.Token, unicode.IsControl) >= 0 {
		return nil, errors.New("notify: ntfy token is invalid")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &NtfyChannel{
		endpoint: endpoint,
		token:    options.Token,
		client:   client,
	}, nil
}

// Kind implements Channel.
func (*NtfyChannel) Kind() ChannelKind {
	return ChannelNtfy
}

// Send implements Channel.
func (c *NtfyChannel) Send(
	ctx context.Context,
	delivery Delivery,
) (SendResult, error) {
	body := []byte(formatNtfyNotification(delivery.Notification))
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return SendResult{
			Outcome: SendPermanent,
			Code:    "invalid_request",
		}, nil
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	request.Header.Set("Title", "Drove: agent blocked")
	request.Header.Set("Priority", "high")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}

	response, err := c.client.Do(request)
	if err != nil {
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
	return classifyHTTPResponse(response, false), nil
}

func ntfyEndpoint(baseURL, topic string) (string, error) {
	if baseURL == "" || strings.TrimSpace(baseURL) != baseURL {
		return "", errors.New("notify: ntfy base URL is invalid")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return "", errors.New("notify: ntfy base URL is invalid")
	}
	if parsed.Scheme != "https" &&
		(parsed.Scheme != "http" || !isLoopbackAddress(parsed.Hostname())) {
		return "", errors.New("notify: ntfy base URL must use HTTPS or loopback HTTP")
	}
	if !validNtfyTopic(topic) {
		return "", errors.New("notify: ntfy topic is invalid")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/" + topic
	parsed.RawPath = ""
	return parsed.String(), nil
}

func validNtfyTopic(topic string) bool {
	if len(topic) == 0 || len(topic) > 64 {
		return false
	}
	for _, char := range topic {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			char == '-' ||
			char == '_' {
			continue
		}
		return false
	}
	return true
}

func isLoopbackAddress(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func formatNtfyNotification(notification Notification) string {
	return fmt.Sprintf(
		"Agent: %s\nVendor: %s\nState: %s\nBlocked event: %d\nOpen: %s",
		notification.AgentName,
		notification.Vendor,
		notification.State,
		notification.BlockedSeq,
		notification.DeepLink,
	)
}
