package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Duang777/drove/internal/auth"
)

func TestSendInputPostsDataAndAcceptsNoContent(t *testing.T) {
	var calls atomic.Int32
	dataDir := t.TempDir()
	token, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.EscapedPath() != "/api/v1/agents/agent%2Fone/input" {
			t.Errorf("path = %q, want escaped agent path", r.URL.EscapedPath())
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q, want application/json", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("authorization = %q, want bearer token", got)
		}
		var body inputRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Data != "continue\n" {
			t.Errorf("data = %q, want %q", body.Data, "continue\n")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := New(
		strings.TrimPrefix(server.URL, "http://"),
		WithTokenFile(auth.TokenPath(dataDir)),
	)
	if err := c.SendInput(context.Background(), "agent/one", []byte("continue\n")); err != nil {
		t.Fatalf("send input: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestSendInputRejectsInvalidUTF8BeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	err := c.SendInput(context.Background(), "agent", []byte{0xff})
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("send input error = %v, want UTF-8 error", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("calls = %d, want 0", calls.Load())
	}
}

func TestSendInputReturnsServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"session: agent is not attached to a PTY"}`, http.StatusConflict)
	}))
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	err := c.SendInput(context.Background(), "agent", []byte("continue\n"))
	if err == nil || !strings.Contains(err.Error(), "409 Conflict") {
		t.Fatalf("send input error = %v, want 409 context", err)
	}
}

func TestDaemonArgsPreserveResolvedConfigPath(t *testing.T) {
	path := "/tmp/drove profile/config.json"
	args := daemonArgs(path)
	if len(args) != 2 || args[0] != "--config" || args[1] != path {
		t.Fatalf("daemon args = %#v, want exact config path", args)
	}
}

func TestDaemonLogUsesPrivateConfiguredDataDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	client := New(
		"127.0.0.1:7373",
		WithTokenFile(auth.TokenPath(dataDir)),
	)
	logPath := client.daemonLogPath()
	if want := filepath.Join(dataDir, "drove.log"); logPath != want {
		t.Fatalf("daemon log path = %q, want %q", logPath, want)
	}
	file, err := openDaemonLog(logPath)
	if err != nil {
		t.Fatalf("open daemon log: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close daemon log: %v", err)
	}

	dirInfo, err := os.Lstat(dataDir)
	if err != nil {
		t.Fatalf("inspect daemon log directory: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("daemon log directory mode = %04o, want 0700", dirInfo.Mode().Perm())
	}
	fileInfo, err := os.Lstat(logPath)
	if err != nil {
		t.Fatalf("inspect daemon log: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("daemon log mode = %04o, want 0600", fileInfo.Mode().Perm())
	}
}

func TestDaemonLogDoesNotChangeExistingDirectoryMode(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatalf("create existing data directory: %v", err)
	}
	if err := os.Chmod(dataDir, 0o755); err != nil {
		t.Fatalf("set existing data directory mode: %v", err)
	}
	file, err := openDaemonLog(filepath.Join(dataDir, "drove.log"))
	if err != nil {
		t.Fatalf("open daemon log: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close daemon log: %v", err)
	}
	info, err := os.Lstat(dataDir)
	if err != nil {
		t.Fatalf("inspect existing data directory: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("existing data directory mode = %04o, want unchanged 0755", info.Mode().Perm())
	}
}

func TestEnsureDaemonDoesNotAutoStartAfterUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	err := c.EnsureDaemon(context.Background(), "/tmp/config.json")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ensure daemon error = %v, want ErrUnauthorized", err)
	}
	if errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("unauthorized error was treated as unreachable: %v", err)
	}
}

func TestPingProbesWithoutMissingTokenFile(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	c := New(
		strings.TrimPrefix(server.URL, "http://"),
		WithTokenFile(auth.TokenPath(t.TempDir())),
	)
	err := c.Ping(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ping error = %v, want ErrUnauthorized", err)
	}
	if authorization != "" {
		t.Fatalf("authorization = %q, want absent during first probe", authorization)
	}
}

func TestListFailsBeforeRequestWhenTokenFileIsInvalid(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	path := auth.TokenPath(t.TempDir())
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatalf("write invalid token: %v", err)
	}
	c := New(strings.TrimPrefix(server.URL, "http://"), WithTokenFile(path))
	if _, err := c.List(context.Background()); err == nil {
		t.Fatal("list succeeded with invalid token")
	}
	if calls.Load() != 0 {
		t.Fatalf("server calls = %d, want 0", calls.Load())
	}
}
