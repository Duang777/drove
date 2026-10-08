// Package config 提供 Drove 的统一配置模型与加载校验。
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxTerminationGraceSeconds int64 = 9_223_372_036
const maxNotifyDebounceSeconds int64 = 9_223_372_036

// SignalInjection controls process-local vendor signal configuration.
type SignalInjection string

const (
	// SignalInjectionAuto enables an adapter's supported session mechanism.
	SignalInjectionAuto SignalInjection = "auto"
	// SignalInjectionOff preserves the vendor command without injection.
	SignalInjectionOff SignalInjection = "off"
)

// AgentConfig contains vendor-keyed launch behavior.
type AgentConfig struct {
	SignalInjection SignalInjection `json:"signal_injection,omitempty"`
}

// StorageConfig controls retention for non-projection attachment data.
type StorageConfig struct {
	// OutputRetentionDays is the number of days to retain raw output. Zero keeps it.
	OutputRetentionDays int `json:"output_retention_days"`
}

// SessionConfig controls daemon session recovery and process shutdown.
type SessionConfig struct {
	AutoResumeOnStart       bool  `json:"auto_resume_on_start"`
	TerminationGraceSeconds int64 `json:"termination_grace_seconds"`
}

// NotifyConfig controls durable notifications for committed Agent states.
type NotifyConfig struct {
	On              []string      `json:"on"`
	DebounceSeconds int64         `json:"debounce_seconds"`
	QuietWhenActive bool          `json:"quiet_when_active"`
	WebPush         WebPushConfig `json:"web_push"`
	Ntfy            NtfyConfig    `json:"ntfy"`
}

// WebPushConfig controls browser Push API delivery.
type WebPushConfig struct {
	Enabled      bool   `json:"enabled"`
	VAPIDSubject string `json:"vapid_subject"`
}

// NtfyConfig controls delivery to one ntfy topic.
type NtfyConfig struct {
	Enabled   bool   `json:"enabled"`
	BaseURL   string `json:"base_url"`
	Topic     string `json:"topic"`
	TokenFile string `json:"token_file"`
}

// Enabled reports whether any notification channel is enabled.
func (c NotifyConfig) Enabled() bool {
	return c.WebPush.Enabled || c.Ntfy.Enabled
}

// Config 是 Drove 的运行时配置。
type Config struct {
	// DataDir 存放 SQLite 事件日志与工作区数据。
	DataDir string `json:"data_dir"`
	// APIBind 是 daemon REST/WebSocket 监听地址。
	APIBind string `json:"api_bind"`
	// DisableTCP 关闭浏览器使用的 loopback TCP listener。
	DisableTCP bool `json:"disable_tcp,omitempty"`
	// EventBuffer 是每个事件订阅者的缓冲行数。
	EventBuffer int `json:"event_buffer"`
	// ConsoleOrigins 是允许建立 WebSocket 的本地控制台 Origin。
	ConsoleOrigins []string `json:"console_origins"`
	// DBPath 是 SQLite 文件路径（由 DataDir 派生，可不配置）。
	DBPath string `json:"db_path,omitempty"`
	// Agents contains vendor-keyed launch behavior without vendor semantics.
	Agents map[string]AgentConfig `json:"agents,omitempty"`
	// Storage controls retention for raw attachment data.
	Storage StorageConfig `json:"storage"`
	// Session controls automatic recovery and PTY shutdown behavior.
	Session SessionConfig `json:"session"`
	// Notify controls durable out-of-process notifications.
	Notify NotifyConfig `json:"notify"`
}

// Defaults 返回安全默认配置。
func Defaults() *Config {
	return &Config{
		DataDir:     defaultDataDir(),
		APIBind:     "127.0.0.1:7373",
		EventBuffer: 1024,
		ConsoleOrigins: []string{
			"http://localhost:5173",
			"http://127.0.0.1:5173",
		},
		Storage: StorageConfig{
			OutputRetentionDays: 30,
		},
		Session: SessionConfig{
			TerminationGraceSeconds: 5,
		},
		Notify: NotifyConfig{
			On:              []string{"blocked"},
			DebounceSeconds: 30,
			QuietWhenActive: true,
		},
	}
}

// DefaultPath 返回 drove init 与隐式加载共享的默认配置路径。
func DefaultPath() string {
	return filepath.Join(defaultDataDir(), "config.json")
}

// defaultDataDir 按平台返回默认数据目录（$HOME/.drove 或 /tmp 兜底）。
func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "drove")
	}
	return filepath.Join(home, ".drove")
}

// Load 从 path 读取配置。空路径使用 DefaultPath，文件不存在则返回默认配置。
func Load(path string) (*Config, error) {
	cfg, _, err := LoadResolved(path)
	return cfg, err
}

// LoadResolved 加载配置并返回传给 daemon 的绝对配置路径。
// 环境变量 DROVE_DATA_DIR 可覆盖 DataDir。
func LoadResolved(path string) (*Config, string, error) {
	if path == "" {
		path = DefaultPath()
	}
	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		return nil, "", fmt.Errorf("config: resolve %q: %w", path, err)
	}

	cfg := Defaults()
	raw, err := os.ReadFile(resolvedPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("config: read %q: %w", resolvedPath, err)
	}
	if err == nil {
		if err := json.Unmarshal(raw, cfg); err != nil {
			return nil, "", fmt.Errorf("config: parse %q: %w", resolvedPath, err)
		}
	}
	if dir := os.Getenv("DROVE_DATA_DIR"); dir != "" {
		cfg.DataDir = dir
	}
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "drove.db")
	}
	return cfg, resolvedPath, nil
}

// Validate 检查配置的合法性，返回首个错误。
func (c *Config) Validate() error {
	if c.DataDir == "" {
		return fmt.Errorf("config: data_dir must not be empty")
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return fmt.Errorf("config: create data_dir: %w", err)
	}
	if c.APIBind == "" {
		return fmt.Errorf("config: api_bind must not be empty")
	}
	host, port, err := net.SplitHostPort(c.APIBind)
	if err != nil {
		return fmt.Errorf("config: api_bind %q: %w", c.APIBind, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("config: api_bind host %q must be loopback", host)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("config: api_bind port %q must be between 1 and 65535", port)
	}
	if c.EventBuffer <= 0 {
		return fmt.Errorf("config: event_buffer must be positive")
	}
	if c.Storage.OutputRetentionDays < 0 {
		return fmt.Errorf("config: storage.output_retention_days must not be negative")
	}
	if c.Session.TerminationGraceSeconds < 0 {
		return fmt.Errorf("config: session.termination_grace_seconds must not be negative")
	}
	if c.Session.TerminationGraceSeconds > maxTerminationGraceSeconds {
		return fmt.Errorf(
			"config: session.termination_grace_seconds must not exceed %d",
			maxTerminationGraceSeconds,
		)
	}
	if err := c.Notify.validate(); err != nil {
		return err
	}
	if len(c.ConsoleOrigins) == 0 {
		return fmt.Errorf("config: console_origins must not be empty")
	}
	seenOrigins := make(map[string]struct{}, len(c.ConsoleOrigins))
	for _, origin := range c.ConsoleOrigins {
		if err := validateConsoleOrigin(origin); err != nil {
			return err
		}
		if _, exists := seenOrigins[origin]; exists {
			return fmt.Errorf("config: duplicate console origin %q", origin)
		}
		seenOrigins[origin] = struct{}{}
	}
	for vendor, settings := range c.Agents {
		if vendor == "" || strings.TrimSpace(vendor) != vendor {
			return fmt.Errorf("config: agent vendor %q is invalid", vendor)
		}
		switch settings.SignalInjection {
		case "", SignalInjectionAuto, SignalInjectionOff:
		default:
			return fmt.Errorf(
				"config: agents.%s.signal_injection must be auto or off",
				vendor,
			)
		}
	}
	return nil
}

func (c NotifyConfig) validate() error {
	if len(c.On) == 0 {
		if c.Enabled() {
			return fmt.Errorf(`config: notify.on must contain only "blocked"`)
		}
	} else if len(c.On) != 1 || c.On[0] != "blocked" {
		return fmt.Errorf(`config: notify.on must contain only "blocked"`)
	}
	if c.DebounceSeconds < 0 {
		return fmt.Errorf("config: notify.debounce_seconds must not be negative")
	}
	if c.DebounceSeconds > maxNotifyDebounceSeconds {
		return fmt.Errorf(
			"config: notify.debounce_seconds must not exceed %d",
			maxNotifyDebounceSeconds,
		)
	}
	if c.WebPush.Enabled {
		if err := validateVAPIDSubject(c.WebPush.VAPIDSubject); err != nil {
			return err
		}
	}
	if c.Ntfy.Enabled {
		if err := validateNtfyBaseURL(c.Ntfy.BaseURL); err != nil {
			return err
		}
		if !validNtfyTopic(c.Ntfy.Topic) {
			return fmt.Errorf(
				"config: notify.ntfy.topic must be 1-64 ASCII letters, digits, hyphens, or underscores",
			)
		}
		if c.Ntfy.TokenFile != "" && !filepath.IsAbs(c.Ntfy.TokenFile) {
			return fmt.Errorf("config: notify.ntfy.token_file must be an absolute path")
		}
	}
	return nil
}

func validateVAPIDSubject(subject string) error {
	if subject == "" || strings.TrimSpace(subject) != subject {
		return fmt.Errorf(
			"config: notify.web_push.vapid_subject must be an HTTPS or mailto URI",
		)
	}
	parsed, err := url.Parse(subject)
	if err != nil {
		return fmt.Errorf("config: notify.web_push.vapid_subject: %w", err)
	}
	switch parsed.Scheme {
	case "https":
		if parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf(
				"config: notify.web_push.vapid_subject must be an HTTPS or mailto URI",
			)
		}
	case "mailto":
		if parsed.Opaque == "" ||
			parsed.Host != "" ||
			parsed.User != nil ||
			parsed.RawQuery != "" ||
			parsed.Fragment != "" {
			return fmt.Errorf(
				"config: notify.web_push.vapid_subject must be an HTTPS or mailto URI",
			)
		}
	default:
		return fmt.Errorf(
			"config: notify.web_push.vapid_subject must be an HTTPS or mailto URI",
		)
	}
	return nil
}

func validateNtfyBaseURL(raw string) error {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return fmt.Errorf(
			"config: notify.ntfy.base_url must be HTTPS or loopback HTTP",
		)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("config: notify.ntfy.base_url: %w", err)
	}
	if parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return fmt.Errorf(
			"config: notify.ntfy.base_url must be HTTPS or loopback HTTP",
		)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf(
		"config: notify.ntfy.base_url must be HTTPS or loopback HTTP",
	)
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

// SignalInjectionFor resolves one vendor setting against adapter capability.
func (c *Config) SignalInjectionFor(
	vendor string,
	supported bool,
) SignalInjection {
	if settings, ok := c.Agents[vendor]; ok && settings.SignalInjection != "" {
		return settings.SignalInjection
	}
	if supported {
		return SignalInjectionAuto
	}
	return SignalInjectionOff
}

func validateConsoleOrigin(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("config: console origin %q: %w", origin, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Path != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return fmt.Errorf("config: console origin %q must be an HTTP origin", origin)
	}
	if !isLoopbackHost(parsed.Hostname()) {
		if parsed.Scheme == "http" {
			return fmt.Errorf(
				"config: HTTP console origin host %q must be loopback",
				parsed.Hostname(),
			)
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
