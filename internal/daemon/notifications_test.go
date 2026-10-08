package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/config"
)

func TestRunStartsAndStopsConfiguredNotificationService(t *testing.T) {
	dataDir := shortDaemonDataDir(t)
	controlToken, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure control token: %v", err)
	}
	cfg := config.Defaults()
	cfg.DataDir = dataDir
	cfg.DBPath = filepath.Join(dataDir, "drove.db")
	cfg.APIBind = reserveAddress(t)
	cfg.Notify.Ntfy = config.NtfyConfig{
		Enabled: true,
		BaseURL: "http://127.0.0.1:8787",
		Topic:   "drove_test",
	}
	cfg.Notify.WebPush = config.WebPushConfig{
		Enabled:      true,
		VAPIDSubject: "mailto:drove@example.com",
	}

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- New(cfg).Run(ctx)
	}()
	t.Cleanup(cancel)
	waitForAPI(t, cfg.APIBind, controlToken)

	notifyPath := filepath.Join(dataDir, "notify.db")
	info, err := os.Lstat(notifyPath)
	if err != nil {
		t.Fatalf("inspect notification database: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("notification database mode = %v, want regular", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("notification database mode = %04o, want 0600", info.Mode().Perm())
	}
	actionKeyPath := filepath.Join(dataDir, "notify", "action-ticket.key")
	actionKeyInfo, err := os.Lstat(actionKeyPath)
	if err != nil {
		t.Fatalf("inspect action ticket key: %v", err)
	}
	if !actionKeyInfo.Mode().IsRegular() {
		t.Fatalf("action ticket key mode = %v, want regular", actionKeyInfo.Mode())
	}
	if runtime.GOOS != "windows" && actionKeyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("action ticket key mode = %04o, want 0600", actionKeyInfo.Mode().Perm())
	}

	cancel()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("daemon run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop notification service")
	}
}

func TestRunRejectsInvalidNtfyTokenWithoutLeakingIt(t *testing.T) {
	const secret = "SECRET-NTFY-TOKEN"
	dataDir := shortDaemonDataDir(t)
	tokenPath := filepath.Join(dataDir, "ntfy.token")
	if err := os.WriteFile(tokenPath, []byte(secret+"\nsecond-line\n"), 0o600); err != nil {
		t.Fatalf("write ntfy token: %v", err)
	}
	cfg := config.Defaults()
	cfg.DataDir = dataDir
	cfg.DBPath = filepath.Join(dataDir, "drove.db")
	cfg.APIBind = reserveAddress(t)
	cfg.Notify.Ntfy = config.NtfyConfig{
		Enabled:   true,
		BaseURL:   "https://ntfy.example.com",
		Topic:     "drove",
		TokenFile: tokenPath,
	}

	err := New(cfg).Run(context.Background())
	if err == nil {
		t.Fatal("daemon accepted an invalid ntfy token")
	}
	if !strings.Contains(err.Error(), "ntfy credentials") {
		t.Fatalf("daemon error = %v, want ntfy credentials", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("daemon error leaked ntfy token: %v", err)
	}
}
