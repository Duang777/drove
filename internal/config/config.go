// Package config 提供 Drove 的统一配置模型与加载校验。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config 是 Drove 的运行时配置。
type Config struct {
	// DataDir 存放 SQLite 事件日志与工作区数据。
	DataDir string `json:"data_dir"`
	// APIBind 是 daemon REST/WebSocket 监听地址。
	APIBind string `json:"api_bind"`
	// EventBuffer 是每个事件订阅者的缓冲行数。
	EventBuffer int `json:"event_buffer"`
	// DBPath 是 SQLite 文件路径（由 DataDir 派生，可不配置）。
	DBPath string `json:"db_path,omitempty"`
}

// Defaults 返回安全默认配置。
func Defaults() *Config {
	return &Config{
		DataDir:     defaultDataDir(),
		APIBind:     "127.0.0.1:7373",
		EventBuffer: 1024,
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
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return fmt.Errorf("config: create data_dir: %w", err)
	}
	if c.APIBind == "" {
		return fmt.Errorf("config: api_bind must not be empty")
	}
	if c.EventBuffer <= 0 {
		return fmt.Errorf("config: event_buffer must be positive")
	}
	return nil
}
