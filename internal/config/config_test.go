package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadResolvedUsesDefaultConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DROVE_DATA_DIR", "")

	configPath := filepath.Join(home, ".drove", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
		"data_dir": "/tmp/drove-from-config",
		"api_bind": "127.0.0.1:8737",
		"event_buffer": 64
	}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, resolvedPath, err := LoadResolved("")
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	if resolvedPath != configPath {
		t.Fatalf("resolved path = %q, want %q", resolvedPath, configPath)
	}
	if cfg.DataDir != "/tmp/drove-from-config" {
		t.Fatalf("data dir = %q, want config value", cfg.DataDir)
	}
	if cfg.APIBind != "127.0.0.1:8737" {
		t.Fatalf("API bind = %q, want config value", cfg.APIBind)
	}
	if cfg.EventBuffer != 64 {
		t.Fatalf("event buffer = %d, want 64", cfg.EventBuffer)
	}
	if cfg.DBPath != "/tmp/drove-from-config/drove.db" {
		t.Fatalf("DB path = %q, want derived config path", cfg.DBPath)
	}
}

func TestLoadResolvedAllowsMissingDefaultConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DROVE_DATA_DIR", "")

	cfg, resolvedPath, err := LoadResolved("")
	if err != nil {
		t.Fatalf("load missing default config: %v", err)
	}
	wantPath := filepath.Join(home, ".drove", "config.json")
	if resolvedPath != wantPath {
		t.Fatalf("resolved path = %q, want %q", resolvedPath, wantPath)
	}
	if cfg.APIBind != Defaults().APIBind {
		t.Fatalf("API bind = %q, want default %q", cfg.APIBind, Defaults().APIBind)
	}
}

func TestLoadResolvedHonorsExplicitPathAndEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	overrideDir := filepath.Join(t.TempDir(), "override")
	t.Setenv("DROVE_DATA_DIR", overrideDir)

	configPath := filepath.Join(t.TempDir(), "custom.json")
	if err := os.WriteFile(configPath, []byte(`{
		"data_dir": "/ignored",
		"api_bind": "127.0.0.1:9737",
		"event_buffer": 32
	}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, resolvedPath, err := LoadResolved(configPath)
	if err != nil {
		t.Fatalf("load explicit config: %v", err)
	}
	if resolvedPath != configPath {
		t.Fatalf("resolved path = %q, want %q", resolvedPath, configPath)
	}
	if cfg.DataDir != overrideDir {
		t.Fatalf("data dir = %q, want environment override %q", cfg.DataDir, overrideDir)
	}
	if cfg.DBPath != filepath.Join(overrideDir, "drove.db") {
		t.Fatalf("DB path = %q, want derived environment path", cfg.DBPath)
	}
}
