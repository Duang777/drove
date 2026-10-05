package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/localipc"
	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/session"
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

func TestCleanupWorktreeUsesAuthenticatedDelete(t *testing.T) {
	dataDir := t.TempDir()
	token, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.EscapedPath() != "/api/v1/worktrees/agent%2Fone" {
			t.Errorf("path = %q, want escaped worktree path", r.URL.EscapedPath())
		}
		if r.URL.RawQuery != "force=true" {
			t.Errorf("query = %q, want force=true", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(
			w,
			`{"agent_id":"agent/one","branch":"feature/isolated"}`,
		)
	}))
	defer server.Close()

	c := New(
		strings.TrimPrefix(server.URL, "http://"),
		WithTokenFile(auth.TokenPath(dataDir)),
	)
	c.hc.Timeout = time.Millisecond
	removed, err := c.CleanupWorktree(
		context.Background(),
		"agent/one",
		true,
	)
	if err != nil {
		t.Fatalf("cleanup worktree: %v", err)
	}
	if removed.AgentID != "agent/one" ||
		removed.Branch != "feature/isolated" {
		t.Fatalf("removed worktree = %+v", removed)
	}
}

func TestCleanupWorktreeReportsOldDaemonRoute(t *testing.T) {
	server := httptest.NewServer(http.NewServeMux())
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	_, err := c.CleanupWorktree(
		context.Background(),
		"11111111-1111-4111-8111-111111111111",
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "restart the daemon") {
		t.Fatalf("cleanup error = %v, want daemon restart guidance", err)
	}
	if IsUserError(err) {
		t.Fatalf("old daemon cleanup error was classified as user error: %v", err)
	}
}

func TestCleanupWorktreeKeepsCurrentDaemonNotFoundAsUserError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		w.Header().Set("Content-Type", "application/json")
		http.Error(
			w,
			`{"error":"session: workspace not found"}`,
			http.StatusNotFound,
		)
	}))
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	_, err := c.CleanupWorktree(
		context.Background(),
		"11111111-1111-4111-8111-111111111111",
		false,
	)
	if err == nil {
		t.Fatal("cleanup succeeded for an unknown workspace")
	}
	if !IsUserError(err) {
		t.Fatalf("current daemon not-found error was not a user error: %v", err)
	}
	if strings.Contains(err.Error(), "restart the daemon") {
		t.Fatalf("current daemon not-found error requested a restart: %v", err)
	}
}

func TestStartUsesDedicatedWorktreeEndpointWithoutClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/worktrees" {
			t.Errorf("path = %q, want worktree endpoint", r.URL.Path)
		}
		var request session.StartRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode start request: %v", err)
		}
		if request.Worktree == nil {
			t.Error("worktree request is missing")
		}
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"agent_id":"worktree-agent"}`)
	}))
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	c.hc.Timeout = time.Millisecond
	status, err := c.Start(context.Background(), session.StartRequest{
		Vendor:   "generic",
		Command:  "/bin/true",
		Worktree: &session.WorktreeRequest{},
	})
	if err != nil {
		t.Fatalf("start worktree session: %v", err)
	}
	if status.AgentID != "worktree-agent" {
		t.Fatalf("worktree status = %+v", status)
	}
}

func TestWorktreeStartDoesNotFallBackToOldAgentEndpoint(t *testing.T) {
	var agentStarts atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/agents", func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		agentStarts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"agent_id":"unsafe-agent"}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	_, err := c.Start(context.Background(), session.StartRequest{
		Vendor:   "generic",
		Command:  "/bin/true",
		Worktree: &session.WorktreeRequest{},
	})
	if err == nil || !strings.Contains(err.Error(), "404 Not Found") {
		t.Fatalf("worktree start error = %v, want 404", err)
	}
	if IsUserError(err) {
		t.Fatalf("old daemon incompatibility was classified as user error: %v", err)
	}
	if !strings.Contains(err.Error(), "restart the daemon") {
		t.Fatalf("worktree start error = %v, want restart guidance", err)
	}
	if agentStarts.Load() != 0 {
		t.Fatalf("old agent endpoint starts = %d, want 0", agentStarts.Load())
	}
}

func TestRotateTokenUsesAuthenticatedPost(t *testing.T) {
	dataDir := t.TempDir()
	token, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/auth/token/rotate" {
			t.Errorf("path = %q, want token rotation endpoint", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := New(
		strings.TrimPrefix(server.URL, "http://"),
		WithTokenFile(auth.TokenPath(dataDir)),
	)
	if err := c.RotateToken(context.Background()); err != nil {
		t.Fatalf("rotate token: %v", err)
	}
}

func TestIssueLoginCodeUsesAuthenticatedPost(t *testing.T) {
	dataDir := t.TempDir()
	token, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/auth/login-code" {
			t.Errorf("path = %q, want login-code endpoint", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":"one-time-code"}`)
	}))
	defer server.Close()

	c := New(
		strings.TrimPrefix(server.URL, "http://"),
		WithTokenFile(auth.TokenPath(dataDir)),
	)
	code, err := c.IssueLoginCode(context.Background())
	if err != nil {
		t.Fatalf("issue login code: %v", err)
	}
	if code != "one-time-code" {
		t.Fatalf("code = %q, want one-time-code", code)
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

func TestResumePostsEscapedAgentPathAndDecodesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.URL.EscapedPath(); got != "/api/v1/agents/agent%2Fone/resume" {
			t.Errorf("path = %q, want escaped resume path", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"agent_id":"agent/one",
			"name":"agent",
			"vendor":"claude",
			"mode":"interactive",
			"state":"working",
			"created_at":"2026-10-04T15:00:00Z",
			"updated_at":"2026-10-04T15:00:01Z",
			"hook_policy":"off",
			"hook_status":"off",
			"signal_injection":"off",
			"signal_injection_status":"off",
			"signal_injection_reason":"hook_policy_off",
			"resumable":false
		}`)
	}))
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	status, err := c.Resume(context.Background(), "agent/one")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if status.AgentID != "agent/one" || status.State != "working" || status.Resumable {
		t.Fatalf("resume status = %+v", status)
	}
}

func TestExplainEncodesPathAndOptionalLimit(t *testing.T) {
	tests := []struct {
		name      string
		options   session.ExplainOptions
		wantQuery string
	}{
		{name: "default omits limit"},
		{
			name:      "explicit limit",
			options:   session.ExplainOptions{Limit: 17},
			wantQuery: "limit=17",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				if got := r.URL.EscapedPath(); got != "/api/v1/agents/agent%2Fone/explain" {
					t.Errorf("path = %q, want escaped agent path", got)
				}
				if r.URL.RawQuery != test.wantQuery {
					t.Errorf("query = %q, want %q", r.URL.RawQuery, test.wantQuery)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{
					"agent_id":"agent/one",
					"state":"blocked",
					"hook_status":"off",
					"attached":false,
					"events":[{"seq":9,"timestamp":"2026-10-04T10:00:00Z","type":"agent.signal","unsupported_version":7}]
				}`)
			}))
			defer server.Close()

			c := New(strings.TrimPrefix(server.URL, "http://"))
			explanation, err := c.Explain(
				context.Background(),
				"agent/one",
				test.options,
			)
			if err != nil {
				t.Fatalf("explain: %v", err)
			}
			if explanation.AgentID != "agent/one" ||
				len(explanation.Events) != 1 ||
				explanation.Events[0].UnsupportedVersion == nil ||
				*explanation.Events[0].UnsupportedVersion != 7 {
				t.Fatalf("explanation = %+v", explanation)
			}
		})
	}
}

func TestTimelineAndBlockedOccurrenceEncodePaths(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.EscapedPath() {
		case "/api/v1/agents/agent%2Fone/timeline":
			_, _ = io.WriteString(w, `{
				"session_id":"agent/one",
				"agent_id":"agent/one",
				"captured":{"seq":"9","next_offset":"12"},
				"captured_at":"2026-10-04T10:00:00Z",
				"duration_ms":40000,
				"output":{"range":{"start":"0","end":"12"},"retained":[],"missing":[]},
				"spans":[],
				"blocked":[]
			}`)
		case "/api/v1/agents/agent%2Fone/timeline/blocked/2":
			_, _ = io.WriteString(w, `{
				"number":2,
				"span":{"state":"blocked","start":{"seq":"8","next_offset":"12"},"start_at":"2026-10-04T09:59:50Z"},
				"jump":{"seq":"3","next_offset":"4"},
				"frame_available":true
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c := New(strings.TrimPrefix(server.URL, "http://"))
	timeline, err := c.Timeline(context.Background(), "agent/one")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if timeline.Captured != (recording.Cursor{Seq: 9, NextOffset: 12}) {
		t.Fatalf("timeline = %+v", timeline)
	}
	blocked, err := c.BlockedOccurrence(context.Background(), "agent/one", 2)
	if err != nil {
		t.Fatalf("blocked occurrence: %v", err)
	}
	if blocked.Number != 2 ||
		blocked.Jump != (recording.Cursor{Seq: 3, NextOffset: 4}) {
		t.Fatalf("blocked occurrence = %+v", blocked)
	}
	if _, err := c.BlockedOccurrence(
		context.Background(),
		"agent/one",
		0,
	); !errors.Is(err, recording.ErrInvalidBlockedOccurrence) {
		t.Fatalf("zero blocked occurrence error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestFrameEncodesEachSelector(t *testing.T) {
	base := time.Date(2026, time.October, 4, 10, 0, 0, 123, time.UTC)
	sequence := recording.Seq(9007199254740993)
	offset := recording.OutputOffset(17)
	tests := []struct {
		name      string
		input     recording.SelectorInput
		wantQuery string
	}{
		{
			name:      "sequence",
			input:     recording.SelectorInput{Seq: &sequence},
			wantQuery: "seq=9007199254740993",
		},
		{
			name:      "time",
			input:     recording.SelectorInput{At: &base},
			wantQuery: "at=2026-10-04T10%3A00%3A00.000000123Z",
		},
		{
			name:      "offset",
			input:     recording.SelectorInput{Offset: &offset},
			wantQuery: "offset=17",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selector, err := recording.NewSelector(test.input)
			if err != nil {
				t.Fatalf("new selector: %v", err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.EscapedPath() != "/api/v1/agents/agent%2Fone/frame" {
					t.Errorf("path = %q", r.URL.EscapedPath())
				}
				if r.URL.RawQuery != test.wantQuery {
					t.Errorf("query = %q, want %q", r.URL.RawQuery, test.wantQuery)
				}
				_, _ = io.WriteString(w, `{
					"session_id":"agent/one",
					"cursor":{"seq":"4","next_offset":"17"},
					"rows":40,
					"columns":120,
					"lines":["done"],
					"truncated":false,
					"fidelity":"exact_origin_replay",
					"restorable":false
				}`)
			}))
			defer server.Close()

			c := New(strings.TrimPrefix(server.URL, "http://"))
			frame, err := c.Frame(context.Background(), "agent/one", selector)
			if err != nil {
				t.Fatalf("frame: %v", err)
			}
			if frame.Cursor != (recording.Cursor{Seq: 4, NextOffset: 17}) ||
				len(frame.Lines) != 1 ||
				frame.Lines[0] != "done" {
				t.Fatalf("frame = %+v", frame)
			}
		})
	}
}

func TestFrameRejectsCursorSelectorBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	cursor := recording.Origin
	selector, err := recording.NewSelector(recording.SelectorInput{Cursor: &cursor})
	if err != nil {
		t.Fatalf("cursor selector: %v", err)
	}
	c := New(strings.TrimPrefix(server.URL, "http://"))
	if _, err := c.Frame(
		context.Background(),
		"agent",
		selector,
	); !errors.Is(err, recording.ErrInvalidFrameSelector) {
		t.Fatalf("cursor frame error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("calls = %d, want 0", calls.Load())
	}
}

func TestFrameReturnsTypedOutputExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = io.WriteString(w, `{
			"error":"recording: output expired",
			"code":"output_expired",
			"session_id":"agent-1",
			"missing":[{"start":"3","end":"8"}]
		}`)
	}))
	defer server.Close()
	sequence := recording.Seq(9)
	selector, err := recording.NewSelector(recording.SelectorInput{Seq: &sequence})
	if err != nil {
		t.Fatalf("selector: %v", err)
	}
	c := New(strings.TrimPrefix(server.URL, "http://"))
	_, err = c.Frame(context.Background(), "agent-1", selector)
	var expired *recording.OutputExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("frame error = %v, want OutputExpiredError", err)
	}
	if expired.SessionID != "agent-1" ||
		len(expired.Missing) != 1 ||
		expired.Missing[0] != (recording.OutputRange{Start: 3, End: 8}) {
		t.Fatalf("expiry = %+v", expired)
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

func TestEnsureDaemonPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := New("127.0.0.1:1")
	err := c.EnsureDaemon(ctx, "/tmp/config.json")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ensure daemon error = %v, want context canceled", err)
	}
	if errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("cancellation was classified as daemon unreachable: %v", err)
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

func TestLocalClientUsesUnixSocketWithoutTCP(t *testing.T) {
	dataDir, err := os.MkdirTemp("/tmp", "drove-client-")
	if err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dataDir); err != nil {
			t.Errorf("remove data directory: %v", err)
		}
	})
	token, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	listener, err := localipc.Listen(dataDir)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != localipc.Authority {
			t.Errorf("host = %q, want %q", r.Host, localipc.Authority)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte("[]"))
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	localClient := NewLocal(
		dataDir,
		WithTokenFile(auth.TokenPath(dataDir)),
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	statuses, err := localClient.List(ctx)
	if err != nil {
		t.Fatalf("list over Unix socket: %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("statuses = %+v, want empty", statuses)
	}
}
