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
	if cfg.Storage.OutputRetentionDays != 30 {
		t.Fatalf(
			"output retention = %d, want 30",
			cfg.Storage.OutputRetentionDays,
		)
	}
}

func TestInitializeDefaultsBacksUpExistingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DROVE_DATA_DIR", "")

	configPath := DefaultPath()
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	original := []byte("{\"custom\":\"preserve exactly\"}\n")
	if err := os.WriteFile(configPath, original, 0o640); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	backupPath, err := InitializeDefaults("")
	if err != nil {
		t.Fatalf("initialize defaults: %v", err)
	}
	if filepath.Dir(backupPath) != filepath.Dir(configPath) {
		t.Fatalf("backup path = %q, want same directory as config", backupPath)
	}
	if !strings.HasPrefix(
		filepath.Base(backupPath),
		filepath.Base(configPath)+".bak-",
	) {
		t.Fatalf("backup path = %q, want config.json.bak-*", backupPath)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != string(original) {
		t.Fatalf("backup contents = %q, want %q", backup, original)
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("inspect backup: %v", err)
	}
	if backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf(
			"backup mode = %04o, want 0600",
			backupInfo.Mode().Perm(),
		)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load initialized config: %v", err)
	}
	defaults := Defaults()
	if cfg.APIBind != defaults.APIBind ||
		cfg.EventBuffer != defaults.EventBuffer ||
		cfg.Storage.OutputRetentionDays !=
			defaults.Storage.OutputRetentionDays {
		t.Fatalf("initialized config = %+v, want defaults", cfg)
	}

	next := []byte("{\"custom\":\"preserve next version\"}\n")
	if err := os.WriteFile(configPath, next, 0o600); err != nil {
		t.Fatalf("write next config: %v", err)
	}
	nextBackupPath, err := InitializeDefaults(configPath)
	if err != nil {
		t.Fatalf("initialize defaults again: %v", err)
	}
	if nextBackupPath == backupPath {
		t.Fatalf("second initialization reused backup path %q", backupPath)
	}
	nextBackup, err := os.ReadFile(nextBackupPath)
	if err != nil {
		t.Fatalf("read next backup: %v", err)
	}
	if string(nextBackup) != string(next) {
		t.Fatalf("next backup contents = %q, want %q", nextBackup, next)
	}
	originalBackup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read original backup again: %v", err)
	}
	if string(originalBackup) != string(original) {
		t.Fatalf(
			"original backup contents = %q, want %q",
			originalBackup,
			original,
		)
	}
}

func TestInitializeDefaultsDoesNotReplaceUnbackablePath(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.Mkdir(configPath, 0o700); err != nil {
		t.Fatalf("create directory at config path: %v", err)
	}

	backupPath, err := InitializeDefaults(configPath)
	if err == nil {
		t.Fatal("initialize defaults succeeded without backing up existing path")
	}
	if backupPath != "" {
		t.Fatalf("backup path = %q, want empty", backupPath)
	}
	info, statErr := os.Stat(configPath)
	if statErr != nil {
		t.Fatalf("inspect original path: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("original path mode = %v, want directory", info.Mode())
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
	if cfg.DisableTCP {
		t.Fatal("TCP is disabled by default")
	}
	if len(cfg.ConsoleOrigins) != 2 ||
		cfg.ConsoleOrigins[0] != "http://localhost:5173" ||
		cfg.ConsoleOrigins[1] != "http://127.0.0.1:5173" {
		t.Fatalf("console origins = %#v", cfg.ConsoleOrigins)
	}
}

func TestLoadResolvedCanDisableTCP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DROVE_DATA_DIR", "")
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"data_dir": "/tmp/drove",
		"api_bind": "127.0.0.1:7373",
		"disable_tcp": true,
		"event_buffer": 16,
		"console_origins": ["http://localhost:5173"]
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, _, err := LoadResolved(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.DisableTCP {
		t.Fatal("TCP remains enabled")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
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

func TestSignalInjectionForUsesCapabilityDefaultAndOverride(t *testing.T) {
	cfg := Defaults()
	if got := cfg.SignalInjectionFor("claude", true); got != SignalInjectionAuto {
		t.Fatalf("supported default = %q, want auto", got)
	}
	if got := cfg.SignalInjectionFor("generic", false); got != SignalInjectionOff {
		t.Fatalf("unsupported default = %q, want off", got)
	}

	cfg.Agents = map[string]AgentConfig{
		"claude": {SignalInjection: SignalInjectionOff},
		"custom": {SignalInjection: SignalInjectionAuto},
	}
	if got := cfg.SignalInjectionFor("claude", true); got != SignalInjectionOff {
		t.Fatalf("explicit off = %q", got)
	}
	if got := cfg.SignalInjectionFor("custom", false); got != SignalInjectionAuto {
		t.Fatalf("explicit auto = %q", got)
	}
}

func TestValidateRejectsInvalidSignalInjection(t *testing.T) {
	cfg := Defaults()
	cfg.DataDir = t.TempDir()
	cfg.Agents = map[string]AgentConfig{
		"codex": {SignalInjection: "required"},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "signal_injection") {
		t.Fatalf("validate error = %v, want signal_injection", err)
	}
}

func TestLoadResolvedParsesAgentSignalInjection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DROVE_DATA_DIR", "")
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"data_dir": "/tmp/drove",
		"api_bind": "127.0.0.1:7373",
		"event_buffer": 16,
		"console_origins": ["http://localhost:5173"],
		"agents": {
			"claude": {"signal_injection": "off"},
			"codex": {"signal_injection": "auto", "future": true}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, _, err := LoadResolved(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
	if cfg.SignalInjectionFor("claude", true) != SignalInjectionOff ||
		cfg.SignalInjectionFor("codex", true) != SignalInjectionAuto {
		t.Fatalf("agent settings = %+v", cfg.Agents)
	}
}

func TestLoadResolvedParsesOutputRetentionAndIgnoresFutureStorageFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DROVE_DATA_DIR", "")
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"data_dir": "/tmp/drove",
		"api_bind": "127.0.0.1:7373",
		"event_buffer": 16,
		"console_origins": ["http://localhost:5173"],
		"storage": {
			"output_retention_days": 0,
			"future_policy": "ignored"
		}
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, _, err := LoadResolved(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Storage.OutputRetentionDays != 0 {
		t.Fatalf("output retention = %d, want permanent", cfg.Storage.OutputRetentionDays)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
}

func TestLoadResolvedParsesSessionResumeSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DROVE_DATA_DIR", "")
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"data_dir": "/tmp/drove",
		"api_bind": "127.0.0.1:7373",
		"event_buffer": 16,
		"console_origins": ["http://localhost:5173"],
		"session": {
			"auto_resume_on_start": true,
			"termination_grace_seconds": 9
		}
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, _, err := LoadResolved(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.Session.AutoResumeOnStart {
		t.Fatal("auto_resume_on_start = false, want true")
	}
	if cfg.Session.TerminationGraceSeconds != 9 {
		t.Fatalf(
			"termination grace = %d, want 9",
			cfg.Session.TerminationGraceSeconds,
		)
	}
	if Defaults().Session.AutoResumeOnStart {
		t.Fatal("default auto_resume_on_start = true, want false")
	}
	if Defaults().Session.TerminationGraceSeconds != 5 {
		t.Fatalf(
			"default termination grace = %d, want 5",
			Defaults().Session.TerminationGraceSeconds,
		)
	}
}

func TestValidateRejectsNegativeOutputRetention(t *testing.T) {
	cfg := Defaults()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.Storage.OutputRetentionDays = -1

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "output_retention_days") {
		t.Fatalf("validate error = %v, want output retention error", err)
	}
}

func TestValidateRejectsNegativeTerminationGrace(t *testing.T) {
	cfg := Defaults()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.Session.TerminationGraceSeconds = -1

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "termination_grace_seconds") {
		t.Fatalf("validate error = %v, want termination grace error", err)
	}
}

func TestValidateRejectsTerminationGraceDurationOverflow(t *testing.T) {
	cfg := Defaults()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.Session.TerminationGraceSeconds = maxTerminationGraceSeconds + 1

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatalf("validate error = %v, want termination grace upper bound", err)
	}
}

func TestValidateCreatesPrivateDataDirectoryWithoutChangingExistingMode(t *testing.T) {
	parent := t.TempDir()
	newPath := filepath.Join(parent, "new-data")
	cfg := Defaults()
	cfg.DataDir = newPath
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate new data directory: %v", err)
	}
	info, err := os.Lstat(newPath)
	if err != nil {
		t.Fatalf("inspect new data directory: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("new data directory mode = %04o, want 0700", info.Mode().Perm())
	}

	existingPath := filepath.Join(parent, "existing-data")
	if err := os.Mkdir(existingPath, 0o755); err != nil {
		t.Fatalf("create existing data directory: %v", err)
	}
	if err := os.Chmod(existingPath, 0o755); err != nil {
		t.Fatalf("set existing data directory mode: %v", err)
	}
	cfg.DataDir = existingPath
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate existing data directory: %v", err)
	}
	info, err = os.Lstat(existingPath)
	if err != nil {
		t.Fatalf("inspect existing data directory: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("existing data directory mode = %04o, want unchanged 0755", info.Mode().Perm())
	}
}

func TestValidateRejectsDataDirectoryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write data path: %v", err)
	}
	cfg := Defaults()
	cfg.DataDir = path
	if err := cfg.Validate(); err == nil {
		t.Fatal("validate accepted a data directory file")
	}
}
