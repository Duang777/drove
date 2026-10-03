package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestDefaultsUseLoopbackBindAndLocalConsoleOrigins(t *testing.T) {
	cfg := Defaults()
	cfg.DataDir = t.TempDir()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate defaults: %v", err)
	}
	if len(cfg.ConsoleOrigins) != 2 ||
		cfg.ConsoleOrigins[0] != "http://localhost:5173" ||
		cfg.ConsoleOrigins[1] != "http://127.0.0.1:5173" {
		t.Fatalf("console origins = %#v", cfg.ConsoleOrigins)
	}
}

func TestValidateRejectsUnsafeAPIBind(t *testing.T) {
	tests := []string{
		"0.0.0.0:7373",
		"[::]:7373",
		":7373",
		"192.0.2.10:7373",
		"localhost",
		"127.0.0.1:0",
		"127.0.0.1:not-a-port",
	}
	for _, bind := range tests {
		t.Run(bind, func(t *testing.T) {
			cfg := Defaults()
			cfg.DataDir = t.TempDir()
			cfg.APIBind = bind
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "api_bind") {
				t.Fatalf("validate %q error = %v, want api_bind error", bind, err)
			}
		})
	}
}

func TestValidateConsoleOrigins(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
		wantErr bool
	}{
		{name: "loopback IPv4", origins: []string{"http://127.0.0.1:4173"}},
		{name: "loopback IPv6", origins: []string{"https://[::1]:4173"}},
		{name: "localhost", origins: []string{"http://localhost"}},
		{name: "empty", origins: nil, wantErr: true},
		{name: "remote host", origins: []string{"https://example.com"}, wantErr: true},
		{name: "path", origins: []string{"http://localhost:5173/"}, wantErr: true},
		{name: "credentials", origins: []string{"http://user@localhost:5173"}, wantErr: true},
		{name: "duplicate", origins: []string{"http://localhost:5173", "http://localhost:5173"}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.DataDir = t.TempDir()
			cfg.ConsoleOrigins = test.origins
			err := cfg.Validate()
			if test.wantErr && err == nil {
				t.Fatal("validate succeeded, want error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}
