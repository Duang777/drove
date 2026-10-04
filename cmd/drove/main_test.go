package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/cliattach"
	"github.com/Duang777/drove/internal/client"
	"github.com/Duang777/drove/internal/config"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/localipc"
	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/workspace"
)

func TestSessionStartRequestMapsRunMode(t *testing.T) {
	tests := []struct {
		name        string
		arg         string
		oneshot     bool
		hooks       agent.HookPolicy
		wantVendor  string
		wantCommand string
		wantMode    agent.RunMode
		useWorktree bool
		branch      string
	}{
		{
			name:       "interactive vendor",
			arg:        "claude",
			wantVendor: "claude",
			wantMode:   agent.RunModeInteractive,
		},
		{
			name:       "oneshot vendor",
			arg:        "codex",
			oneshot:    true,
			hooks:      agent.HooksRequired,
			wantVendor: "codex",
			wantMode:   agent.RunModeOneshot,
		},
		{
			name:        "interactive custom command",
			arg:         "/usr/local/bin/my-agent",
			wantVendor:  "generic",
			wantCommand: "/usr/local/bin/my-agent",
			wantMode:    agent.RunModeInteractive,
		},
		{
			name:        "oneshot custom command",
			arg:         "/usr/local/bin/my-agent",
			oneshot:     true,
			wantVendor:  "generic",
			wantCommand: "/usr/local/bin/my-agent",
			wantMode:    agent.RunModeOneshot,
		},
		{
			name:        "worktree vendor",
			arg:         "claude",
			wantVendor:  "claude",
			wantMode:    agent.RunModeInteractive,
			useWorktree: true,
			branch:      "feature/isolated",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := sessionStartRequest(
				test.arg,
				"name",
				"/tmp",
				test.oneshot,
				test.hooks,
				test.useWorktree,
				test.branch,
			)
			if req.Vendor != test.wantVendor ||
				req.Command != test.wantCommand ||
				req.Mode != test.wantMode ||
				req.Name != "name" ||
				req.Dir != "/tmp" ||
				req.Hooks != test.hooks {
				t.Fatalf("request = %+v", req)
			}
			if test.useWorktree {
				if req.Worktree == nil || req.Worktree.Branch != test.branch {
					t.Fatalf("worktree request = %+v", req.Worktree)
				}
			} else if req.Worktree != nil {
				t.Fatalf("unexpected worktree request = %+v", req.Worktree)
			}
		})
	}
}

func TestInitCreatesPrivateDataDirectoryWithRetentionDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := newInitCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("run init: %v", err)
	}

	dataDir := filepath.Join(home, ".drove")
	info, err := os.Lstat(dataDir)
	if err != nil {
		t.Fatalf("inspect data directory: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("data directory mode = %04o, want 0700", info.Mode().Perm())
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.Storage.OutputRetentionDays != 30 {
		t.Fatalf(
			"output retention = %d, want 30",
			cfg.Storage.OutputRetentionDays,
		)
	}
	if cfg.Session.AutoResumeOnStart ||
		cfg.Session.TerminationGraceSeconds != 5 {
		t.Fatalf("session defaults = %+v", cfg.Session)
	}
}

func TestUpCommandExposesRunnerAndHookFlags(t *testing.T) {
	flags := newUpCmd().Flags()
	oneshot := flags.Lookup("oneshot")
	if oneshot == nil || oneshot.DefValue != "false" {
		t.Fatalf("--oneshot flag = %+v, want default false", oneshot)
	}
	hooks := flags.Lookup("hooks")
	if hooks == nil || hooks.DefValue != "" {
		t.Fatalf("--hooks flag = %+v, want empty default", hooks)
	}
	worktree := flags.Lookup("worktree")
	if worktree == nil || worktree.DefValue != "false" {
		t.Fatalf("--worktree flag = %+v, want default false", worktree)
	}
	branch := flags.Lookup("branch")
	if branch == nil || branch.DefValue != "" {
		t.Fatalf("--branch flag = %+v, want empty default", branch)
	}
}

func TestUpCommandRejectsInvalidHookPolicyBeforeClientSetup(t *testing.T) {
	command := newUpCmd()
	command.SetArgs([]string{"claude", "--hooks", "sometimes"})
	err := command.Execute()
	if !errors.Is(err, session.ErrInvalidHookPolicy) {
		t.Fatalf("up error = %v, want ErrInvalidHookPolicy", err)
	}
}

func TestUpCommandRejectsBranchWithoutWorktreeBeforeClientSetup(t *testing.T) {
	command := newUpCmd()
	command.SetArgs([]string{"claude", "--branch", "feature/isolated"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "--branch requires --worktree") {
		t.Fatalf("up error = %v, want branch dependency error", err)
	}
	if got := commandExitCode(err); got != exitUsage {
		t.Fatalf("exit code = %d, want %d", got, exitUsage)
	}
}

func TestWorktreeRemoveMissingAgentIDIsUsageError(t *testing.T) {
	command := newRootCmd()
	command.SetArgs([]string{"worktree", "rm"})
	err := command.Execute()
	if err == nil {
		t.Fatal("worktree remove succeeded without an Agent ID")
	}
	if got := commandExitCode(err); got != exitUsage {
		t.Fatalf("exit code = %d, want %d for %v", got, exitUsage, err)
	}
}

func TestDaemonBadRequestIsUsageError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		http.Error(w, `{"error":"invalid Agent ID"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	daemonClient := client.New(strings.TrimPrefix(server.URL, "http://"))
	_, err := daemonClient.CleanupWorktree(
		context.Background(),
		"not-an-agent-id",
		false,
	)
	if err == nil {
		t.Fatal("cleanup succeeded after daemon bad request")
	}
	if got := commandExitCode(err); got != exitUsage {
		t.Fatalf("exit code = %d, want %d for %v", got, exitUsage, err)
	}
}

func TestTerminalStreamRequestErrorIsUsageError(t *testing.T) {
	err := fmt.Errorf(
		"attach failed: %w",
		&client.TerminalStreamError{
			Code:    "unknown_agent",
			Message: "agent does not exist",
		},
	)
	if got := commandExitCode(err); got != exitUsage {
		t.Fatalf("exit code = %d, want %d for %v", got, exitUsage, err)
	}
}

func TestTerminalStreamInternalErrorUsesRuntimeExitCode(t *testing.T) {
	err := &client.TerminalStreamError{
		Code:    "internal_error",
		Message: "terminal failed",
	}
	if got := commandExitCode(err); got != exitErr {
		t.Fatalf("exit code = %d, want %d for %v", got, exitErr, err)
	}
}

func TestRuntimeFailureUsesRuntimeExitCode(t *testing.T) {
	err := errors.New("daemon unavailable")
	if got := commandExitCode(err); got != exitErr {
		t.Fatalf("exit code = %d, want %d", got, exitErr)
	}
}

func TestExecuteRootTreatsUnknownCommandsAsUsageErrors(t *testing.T) {
	tests := [][]string{
		{"unknown"},
		{"worktree", "unknown"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			err := executeRoot(newRootCmd(), args)
			if err == nil || !strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("execute %v error = %v", args, err)
			}
			if got := commandExitCode(err); got != exitUsage {
				t.Fatalf("exit code = %d, want %d for %v", got, exitUsage, err)
			}
		})
	}
}

func TestWorktreeCommandIsRegisteredWithForceFlag(t *testing.T) {
	command, _, err := newRootCmd().Find([]string{"worktree", "rm"})
	if err != nil {
		t.Fatalf("find worktree remove command: %v", err)
	}
	if command.Name() != "rm" {
		t.Fatalf("command = %q, want rm", command.Name())
	}
	force := command.Flags().Lookup("force")
	if force == nil || force.DefValue != "false" {
		t.Fatalf("--force flag = %+v, want default false", force)
	}
	for _, want := range []string{
		"未提交更改",
		"detached HEAD",
		"旧版未知保护信息",
	} {
		if !strings.Contains(force.Usage, want) {
			t.Fatalf("--force usage = %q, want %q", force.Usage, want)
		}
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	truncated := truncate("功能分支名称很长", 5)
	if !utf8.ValidString(truncated) {
		t.Fatalf("truncate returned invalid UTF-8: %q", truncated)
	}
	if truncated != "功能分支名…" {
		t.Fatalf("truncate = %q, want %q", truncated, "功能分支名…")
	}
}

func TestWriteWorktrees(t *testing.T) {
	branch := "feature/isolated-worktree-with-a-long-name"
	var output bytes.Buffer
	err := writeWorktrees(&output, []workspace.Workspace{{
		AgentID: "11111111-1111-4111-8111-111111111111",
		Branch:  branch,
		Path:    "/tmp/drove/worktree",
		Dirty:   true,
	}})
	if err != nil {
		t.Fatalf("write worktrees: %v", err)
	}
	for _, want := range []string{
		"AGENT ID",
		branch,
		"true",
		"/tmp/drove/worktree",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output = %q, want %q", output.String(), want)
		}
	}
}

func TestSendCommandIsRegisteredWithStdinFlag(t *testing.T) {
	command, _, err := newRootCmd().Find([]string{"send"})
	if err != nil {
		t.Fatalf("find send command: %v", err)
	}
	if command.Name() != "send" {
		t.Fatalf("command = %q, want send", command.Name())
	}
	flag := command.Flags().Lookup("stdin")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("stdin flag = %+v, want default false", flag)
	}
}

func TestAttachCommandIsRegisteredWithReadOnlyFlag(t *testing.T) {
	command, _, err := newRootCmd().Find([]string{"attach"})
	if err != nil {
		t.Fatalf("find attach command: %v", err)
	}
	if command.Name() != "attach" {
		t.Fatalf("command = %q, want attach", command.Name())
	}
	flag := command.Flags().Lookup("read-only")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("read-only flag = %+v, want default false", flag)
	}
}

func TestAttachCommandMapsAgentAndAccessToRunner(t *testing.T) {
	daemon := &client.Client{}
	var (
		factoryContext context.Context
		runContext     context.Context
		runClient      *client.Client
		runAgentID     string
		runOptions     cliattach.Options
	)
	command := newAttachCmdWith(
		func(ctx context.Context) (*client.Client, error) {
			factoryContext = ctx
			return daemon, nil
		},
		func(
			ctx context.Context,
			gotClient *client.Client,
			agentID string,
			options cliattach.Options,
		) error {
			runContext = ctx
			runClient = gotClient
			runAgentID = agentID
			runOptions = options
			return nil
		},
	)
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("test"), "attach")
	command.SetContext(ctx)
	command.SetArgs([]string{"agent-1", "--read-only"})
	if err := command.Execute(); err != nil {
		t.Fatalf("execute attach: %v", err)
	}
	if factoryContext != ctx || runContext != ctx {
		t.Fatal("attach command did not preserve its context")
	}
	if runClient != daemon || runAgentID != "agent-1" || !runOptions.ReadOnly {
		t.Fatalf(
			"runner args client=%p agent=%q options=%+v",
			runClient,
			runAgentID,
			runOptions,
		)
	}
}

func TestResumeCommandUsesEscapedAgentIDAndPrintsStatus(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "drove-resume-")
	if err != nil {
		t.Fatalf("create home: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Errorf("remove home: %v", err)
		}
	})
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".drove")
	token, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure control token: %v", err)
	}
	listener, err := localipc.Listen(dataDir)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.EscapedPath() {
		case "/api/v1/agents":
			_, _ = io.WriteString(w, "[]")
		case "/api/v1/agents/agent%2Fone/resume":
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			_ = json.NewEncoder(w).Encode(session.Status{
				AgentID: "agent/one",
				Vendor:  "claude",
				Mode:    agent.RunModeInteractive,
				State:   agent.StateWorking,
				PID:     42,
			})
		default:
			http.NotFound(w, r)
		}
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	cfg := config.Defaults()
	cfg.DataDir = dataDir
	cfg.DBPath = ""
	rawConfig, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(config.DefaultPath(), rawConfig, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	command := newResumeCmd()
	command.SetArgs([]string{"agent/one"})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := command.Execute(); err != nil {
		t.Fatalf("execute resume: %v", err)
	}
	if got := stdout.String(); got != "resumed agent agent/one (vendor=claude, mode=interactive, state=working, pid=42)\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestWriteStatusesIncludesResumableColumn(t *testing.T) {
	var output bytes.Buffer
	err := writeStatuses(&output, []*session.Status{
		{
			AgentID:   "agent-1",
			Name:      "first",
			Vendor:    "claude",
			Mode:      agent.RunModeInteractive,
			State:     agent.StateStopped,
			Resumable: true,
		},
	})
	if err != nil {
		t.Fatalf("write statuses: %v", err)
	}
	if !strings.Contains(output.String(), "RESUMABLE") ||
		!strings.Contains(output.String(), "true") {
		t.Fatalf("status output = %q", output.String())
	}
}

func TestHookCommandRelaysInjectedSessionEnvelope(t *testing.T) {
	var received struct {
		Version    int             `json:"version"`
		Vendor     string          `json:"vendor"`
		DeliveryID string          `json:"delivery_id"`
		Payload    json.RawMessage `json:"payload"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/agent-1/signal" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer session-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv(session.SignalAgentIDEnv, "agent-1")
	t.Setenv(session.SignalURLEnv, server.URL+"/api/v1/agents/agent-1/signal")
	t.Setenv(session.SignalTokenEnv, "session-token")
	payload := `{"hook_event_name":"SessionStart","session_id":"vendor-session"}`

	command := newHookCmd()
	command.SetArgs([]string{"--vendor", "claude"})
	command.SetIn(strings.NewReader(payload))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	if err := command.Execute(); err != nil {
		t.Fatalf("execute hook: %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q, want both empty", stdout.String(), stderr.String())
	}
	if received.Version != 1 ||
		received.Vendor != "claude" ||
		received.DeliveryID == "" ||
		string(received.Payload) != payload {
		t.Fatalf("request = %+v", received)
	}
}

func TestHookCommandRelaysArgvPayloadWithManagedMarker(t *testing.T) {
	var received struct {
		Vendor  string          `json:"vendor"`
		Payload json.RawMessage `json:"payload"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv(session.SignalAgentIDEnv, "agent-1")
	t.Setenv(session.SignalURLEnv, server.URL+"/api/v1/agents/agent-1/signal")
	t.Setenv(session.SignalTokenEnv, "session-token")
	payload := `{"type":"agent-turn-complete","thread-id":"thread-1"}`

	command := newHookCmd()
	command.SetArgs([]string{
		"--vendor", "codex",
		"--managed-by", "drove/v1",
		"--payload-argv",
		payload,
	})
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	if err := command.Execute(); err != nil {
		t.Fatalf("execute hook: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if received.Vendor != "codex" || string(received.Payload) != payload {
		t.Fatalf("request = %+v", received)
	}
}

func TestHookCommandValidatesManagedAndPayloadModes(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "invalid managed marker",
			args: []string{"--vendor", "claude", "--managed-by", "other"},
		},
		{
			name: "stdin mode rejects positional payload",
			args: []string{"--vendor", "claude", `{}`},
		},
		{
			name: "argv mode requires payload",
			args: []string{"--vendor", "codex", "--payload-argv"},
		},
		{
			name: "argv mode rejects extra payload",
			args: []string{"--vendor", "codex", "--payload-argv", `{}`, `{}`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := newHookCmd()
			command.SetArgs(test.args)
			if err := command.Execute(); err == nil {
				t.Fatal("hook command succeeded")
			}
		})
	}
}

func TestHookCommandReportsFailureWithoutBlockingVendor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"secret response"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	tests := []struct {
		name      string
		agentID   string
		signalURL string
		token     string
		payload   string
	}{
		{
			name:      "missing environment",
			signalURL: server.URL + "/api/v1/agents/agent-1/signal",
			token:     "session-token",
			payload:   `{}`,
		},
		{
			name:      "URL agent mismatch",
			agentID:   "agent-1",
			signalURL: server.URL + "/api/v1/agents/agent-2/signal",
			token:     "session-token",
			payload:   `{}`,
		},
		{
			name:      "malformed payload",
			agentID:   "agent-1",
			signalURL: server.URL + "/api/v1/agents/agent-1/signal",
			token:     "session-token",
			payload:   `[]`,
		},
		{
			name:      "endpoint rejection",
			agentID:   "agent-1",
			signalURL: server.URL + "/api/v1/agents/agent-1/signal",
			token:     "session-token",
			payload:   `{"secret":"payload"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(session.SignalAgentIDEnv, test.agentID)
			t.Setenv(session.SignalURLEnv, test.signalURL)
			t.Setenv(session.SignalTokenEnv, test.token)
			command := newHookCmd()
			command.SetArgs([]string{"--vendor", "claude"})
			command.SetIn(strings.NewReader(test.payload))
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			if err := command.Execute(); err != nil {
				t.Fatalf("hook error = %v, want nil", err)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if stderr.String() != "drove hook: signal delivery failed\n" {
				t.Fatalf("stderr = %q", stderr.String())
			}
			for _, secret := range []string{
				test.agentID,
				test.signalURL,
				test.token,
				test.payload,
				"secret response",
			} {
				if secret != "" && strings.Contains(stderr.String(), secret) {
					t.Fatalf("stderr exposed %q: %q", secret, stderr.String())
				}
			}
		})
	}
}

func TestHookCommandRequiresVendorFlag(t *testing.T) {
	command := newHookCmd()
	command.SetArgs(nil)
	command.SetIn(strings.NewReader(`{}`))
	if err := command.Execute(); err == nil {
		t.Fatal("hook command succeeded without --vendor")
	}
}

func TestReadSendInput(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		fromStdin  bool
		stdin      []byte
		want       string
		wantErr    error
		wantAnyErr bool
	}{
		{
			name: "positional appends newline",
			args: []string{"agent-1", "continue"},
			want: "continue\n",
		},
		{
			name:      "stdin is unchanged",
			args:      []string{"agent-1"},
			fromStdin: true,
			stdin:     []byte("first\nsecond\n"),
			want:      "first\nsecond\n",
		},
		{
			name:       "stdin and text are exclusive",
			args:       []string{"agent-1", "continue"},
			fromStdin:  true,
			stdin:      []byte("ignored"),
			wantAnyErr: true,
		},
		{
			name:       "positional text is required",
			args:       []string{"agent-1"},
			wantAnyErr: true,
		},
		{
			name:      "empty stdin",
			args:      []string{"agent-1"},
			fromStdin: true,
			wantErr:   session.ErrInputEmpty,
		},
		{
			name:      "oversized stdin",
			args:      []string{"agent-1"},
			fromStdin: true,
			stdin:     []byte(strings.Repeat("x", session.MaxInputBytes+1)),
			wantErr:   session.ErrInputTooLarge,
		},
		{
			name:      "invalid UTF-8 stdin",
			args:      []string{"agent-1"},
			fromStdin: true,
			stdin:     []byte{0xff},
			wantErr:   session.ErrInputNotUTF8,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := readSendInput(test.args, test.fromStdin, bytes.NewReader(test.stdin))
			if test.wantErr != nil || test.wantAnyErr {
				if err == nil {
					t.Fatalf("read input succeeded, want error")
				}
				if test.wantErr == nil {
					return
				}
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("read input error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("input = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWriteLogRowsWritesRawChunksAndLegacyNewline(t *testing.T) {
	retained, err := event.HydrateOutputChunkPayload(
		`{"version":1,"offset":11,"len":6}`,
		[]byte{0x1b, '[', '2', 'J', 0, 0xff},
	)
	if err != nil {
		t.Fatalf("hydrate retained chunk: %v", err)
	}
	rows := []store.EventRow{
		{
			Seq:     1,
			Type:    string(event.TypeOutput),
			Payload: "first line\r\n",
		},
		{
			Seq:     2,
			Type:    string(event.TypeOutputChunk),
			Payload: retained,
		},
		{
			Seq:     3,
			Type:    string(event.TypeOutputChunk),
			Payload: `{"version":1,"offset":17,"len":4}`,
		},
		{
			Seq:    4,
			Type:   string(event.TypeStateChanged),
			Reason: "ready",
		},
	}

	var output bytes.Buffer
	if err := writeLogRows(&output, rows, false); err != nil {
		t.Fatalf("write log rows: %v", err)
	}
	want := append([]byte("first line\n"), []byte{0x1b, '[', '2', 'J', 0, 0xff}...)
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("output = %q, want %q", output.Bytes(), want)
	}
}

func TestWriteLogRowsRejectsMalformedOutputChunk(t *testing.T) {
	var output bytes.Buffer
	err := writeLogRows(&output, []store.EventRow{{
		Seq:     9,
		Type:    string(event.TypeOutputChunk),
		Payload: `{"version":1,"offset":0,"len":2,"data_b64":"YQ=="}`,
	}}, false)
	if err == nil || !strings.Contains(err.Error(), "seq 9") {
		t.Fatalf("write error = %v, want malformed chunk at seq 9", err)
	}
	if output.Len() != 0 {
		t.Fatalf("malformed chunk wrote %q", output.Bytes())
	}
}

func TestLogCommandExposesPlainFlag(t *testing.T) {
	flag := newLogCmd().Flags().Lookup("plain")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("--plain flag = %+v, want default false", flag)
	}
}

func TestExplainCommandIsRegisteredWithFlags(t *testing.T) {
	command, _, err := newRootCmd().Find([]string{"explain"})
	if err != nil {
		t.Fatalf("find explain command: %v", err)
	}
	if command.Name() != "explain" {
		t.Fatalf("command = %q, want explain", command.Name())
	}
	for name, wantDefault := range map[string]string{
		"limit": "0",
		"json":  "false",
	} {
		flag := command.Flags().Lookup(name)
		if flag == nil || flag.DefValue != wantDefault {
			t.Fatalf("--%s flag = %+v, want default %q", name, flag, wantDefault)
		}
	}
}

func TestTimelineCommandIsRegisteredWithJSONFlag(t *testing.T) {
	command, _, err := newRootCmd().Find([]string{"timeline"})
	if err != nil {
		t.Fatalf("find timeline command: %v", err)
	}
	if command.Name() != "timeline" {
		t.Fatalf("command = %q, want timeline", command.Name())
	}
	flag := command.Flags().Lookup("json")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("--json flag = %+v, want default false", flag)
	}
}

func TestWriteTimelinePrintsSpansAndNumberedBlockedEntries(t *testing.T) {
	base := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	end := recording.Cursor{Seq: 9, NextOffset: 12}
	endAt := base.Add(20 * time.Second)
	duration := int64(20000)
	timeline := recording.Timeline{
		AgentID:        "agent-1",
		Captured:       end,
		DurationMillis: 40000,
		Output: recording.OutputCoverage{
			Range: recording.OutputRange{Start: 0, End: 12},
		},
		Spans: []recording.StateSpan{{
			State:          agent.StateBlocked,
			Start:          recording.Cursor{Seq: 7, NextOffset: 8},
			End:            &end,
			StartAt:        base,
			EndAt:          &endAt,
			DurationMillis: &duration,
			Source:         "screen",
			Rule:           "claude.approval_prompt",
			Reason:         "waiting",
		}},
		Blocked: []recording.BlockedOccurrence{{
			Number: 1,
			Span: recording.StateSpan{
				State:          agent.StateBlocked,
				Start:          recording.Cursor{Seq: 7, NextOffset: 8},
				End:            &end,
				StartAt:        base,
				EndAt:          &endAt,
				DurationMillis: &duration,
			},
			Jump:           recording.Cursor{Seq: 3, NextOffset: 4},
			FrameAvailable: true,
		}},
	}
	var output bytes.Buffer
	if err := writeTimeline(&output, timeline); err != nil {
		t.Fatalf("write timeline: %v", err)
	}
	text := output.String()
	for _, fragment := range []string{
		"agent agent-1 captured=9/12 duration=40s output=[0,12)",
		"blocked start=7/8 at=2026-10-04T10:00:00Z end=9/12",
		"source=screen rule=claude.approval_prompt",
		`reason="waiting"`,
		"blocked #1 start=7/8 duration=20s jump=3/4 frame_available=true",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("timeline output missing %q:\n%s", fragment, text)
		}
	}
}

func TestTimelineCommandJSONKeepsStdoutMachineClean(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "drove-timeline-")
	if err != nil {
		t.Fatalf("create home: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Errorf("remove home: %v", err)
		}
	})
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".drove")
	token, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure control token: %v", err)
	}

	listener, err := localipc.Listen(dataDir)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.EscapedPath() {
		case "/api/v1/agents":
			_, _ = io.WriteString(w, "[]")
		case "/api/v1/agents/agent%2Fone/timeline":
			_ = json.NewEncoder(w).Encode(recording.Timeline{
				SessionID:  "agent/one",
				AgentID:    "agent/one",
				Captured:   recording.Cursor{Seq: 9, NextOffset: 12},
				CapturedAt: time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC),
				Output: recording.OutputCoverage{
					Range:    recording.OutputRange{Start: 0, End: 12},
					Retained: []recording.OutputRange{{Start: 0, End: 12}},
					Missing:  []recording.OutputRange{},
				},
				Spans:   []recording.StateSpan{},
				Blocked: []recording.BlockedOccurrence{},
			})
		default:
			http.NotFound(w, r)
		}
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	cfg := config.Defaults()
	cfg.DataDir = dataDir
	cfg.DBPath = ""
	rawConfig, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(config.DefaultPath(), rawConfig, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	command := newTimelineCmd()
	command.SetArgs([]string{"agent/one", "--json"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	if err := command.Execute(); err != nil {
		t.Fatalf("execute timeline: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	decoder := json.NewDecoder(&stdout)
	var timeline recording.Timeline
	if err := decoder.Decode(&timeline); err != nil {
		t.Fatalf("decode stdout: %v; output=%q", err, stdout.String())
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout contains non-JSON data: %q", stdout.String())
	}
	if timeline.AgentID != "agent/one" ||
		timeline.Captured != (recording.Cursor{Seq: 9, NextOffset: 12}) {
		t.Fatalf("timeline = %+v", timeline)
	}
}

func TestWriteExplanationShowsDecisionsAndAttachedScreen(t *testing.T) {
	capturedAt := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	explanation := session.Explanation{
		AgentID:    "agent-1",
		State:      agent.StateWorking,
		HookStatus: detect.HookActive,
		Attached:   true,
		Events: []session.ExplainEvent{
			{
				Seq:               10,
				Timestamp:         capturedAt.Add(-time.Second),
				Type:              event.TypeAgentSignal,
				Source:            agent.EvidenceScreen,
				Kind:              detect.KindHumanInputRequired,
				Outcome:           detect.OutcomeSuppressed,
				Rule:              "claude.approval_prompt",
				Edge:              agent.ScreenEdgePresent,
				Region:            "viewport.bottom",
				Evidence:          "approval prompt",
				SuppressionReason: "detector authority rejected this signal",
			},
			{
				Seq:       11,
				Timestamp: capturedAt,
				Type:      event.TypeStateChanged,
				Source:    agent.EvidenceScreen,
				Rule:      "claude.approval_prompt",
				From:      agent.StateBlocked,
				To:        agent.StateWorking,
				Reason:    "approval resolved",
			},
		},
		Screen: &session.ExplainScreen{
			CapturedAt: capturedAt,
			Rows:       []string{"first", "second"},
			Truncated:  true,
		},
	}

	var output bytes.Buffer
	if err := writeExplanation(&output, explanation); err != nil {
		t.Fatalf("write explanation: %v", err)
	}
	text := output.String()
	firstIndex := strings.Index(text, "10 2026-10-04T09:59:59Z agent.signal")
	secondIndex := strings.Index(text, "11 2026-10-04T10:00:00Z state_changed")
	if firstIndex < 0 || secondIndex <= firstIndex {
		t.Fatalf("events are not chronological:\n%s", text)
	}
	for _, fragment := range []string{
		"source=screen",
		"kind=human_input_required",
		"outcome=suppressed",
		"rule=claude.approval_prompt",
		`suppression_reason="detector authority rejected this signal"`,
		"transition=blocked->working",
		"ephemeral redacted current screen\n",
		"captured_at=2026-10-04T10:00:00Z truncated=true",
		"first\nsecond\n",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("output missing %q:\n%s", fragment, text)
		}
	}
}

func TestWriteExplanationOmitsDetachedScreenPlaceholder(t *testing.T) {
	var output bytes.Buffer
	if err := writeExplanation(&output, session.Explanation{
		AgentID:    "agent-1",
		State:      agent.StateStopped,
		HookStatus: detect.HookDetached,
		Events:     []session.ExplainEvent{},
	}); err != nil {
		t.Fatalf("write explanation: %v", err)
	}
	if strings.Contains(output.String(), "screen") {
		t.Fatalf("detached output contains screen placeholder: %q", output.String())
	}
}

func TestExplainCommandJSONKeepsStdoutMachineClean(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "drove-explain-")
	if err != nil {
		t.Fatalf("create home: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Errorf("remove home: %v", err)
		}
	})
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".drove")
	token, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure control token: %v", err)
	}

	listener, err := localipc.Listen(dataDir)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.EscapedPath() {
		case "/api/v1/agents":
			_, _ = io.WriteString(w, "[]")
		case "/api/v1/agents/agent%2Fone/explain":
			if r.URL.RawQuery != "limit=7" {
				t.Errorf("query = %q, want limit=7", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(session.Explanation{
				AgentID:    "agent/one",
				State:      agent.StateBlocked,
				HookStatus: detect.HookOff,
				Attached:   false,
				Events:     []session.ExplainEvent{},
			})
		default:
			http.NotFound(w, r)
		}
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	cfg := config.Defaults()
	cfg.DataDir = dataDir
	cfg.DBPath = ""
	rawConfig, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(config.DefaultPath(), rawConfig, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	command := newExplainCmd()
	command.SetArgs([]string{"agent/one", "--limit", "7", "--json"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	if err := command.Execute(); err != nil {
		t.Fatalf("execute explain: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	decoder := json.NewDecoder(&stdout)
	var explanation session.Explanation
	if err := decoder.Decode(&explanation); err != nil {
		t.Fatalf("decode stdout: %v; output=%q", err, stdout.String())
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout contains non-JSON data: %q", stdout.String())
	}
	if explanation.AgentID != "agent/one" || explanation.State != agent.StateBlocked {
		t.Fatalf("explanation = %+v", explanation)
	}
}

func TestExplainCommandRejectsInvalidExplicitLimitBeforeClientSetup(t *testing.T) {
	for _, limit := range []string{"0", "-1", "201"} {
		command := newExplainCmd()
		command.SetArgs([]string{"agent-1", "--limit", limit})
		if err := command.Execute(); !errors.Is(err, session.ErrInvalidExplainLimit) {
			t.Fatalf("limit %s error = %v, want ErrInvalidExplainLimit", limit, err)
		}
	}
}

func TestTokenRotateCommandUsesLocalDaemon(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "drove-token-")
	if err != nil {
		t.Fatalf("create home: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Errorf("remove home: %v", err)
		}
	})
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".drove")
	token, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure control token: %v", err)
	}

	listener, err := localipc.Listen(dataDir)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rotateCalls := 0
	server := &http.Server{Handler: http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents":
			_, _ = io.WriteString(w, "[]")
		case r.Method == http.MethodPost &&
			r.URL.Path == "/api/v1/auth/token/rotate":
			rotateCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	cfg := config.Defaults()
	cfg.DataDir = dataDir
	cfg.DBPath = ""
	rawConfig, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(config.DefaultPath(), rawConfig, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	command := newTokenCmd()
	command.SetArgs([]string{"rotate"})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := command.Execute(); err != nil {
		t.Fatalf("execute token rotate: %v", err)
	}
	if rotateCalls != 1 {
		t.Fatalf("rotate calls = %d, want 1", rotateCalls)
	}
	if stdout.String() != "control token rotated\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestWebCommandIssuesCodeAndOpensFragmentURL(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "drove-web-")
	if err != nil {
		t.Fatalf("create home: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Errorf("remove home: %v", err)
		}
	})
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".drove")
	token, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure control token: %v", err)
	}

	listener, err := localipc.Listen(dataDir)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents":
			_, _ = io.WriteString(w, "[]")
		case r.Method == http.MethodPost &&
			r.URL.Path == "/api/v1/auth/login-code":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":"single-use-code"}`)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	cfg := config.Defaults()
	cfg.DataDir = dataDir
	cfg.APIBind = "127.0.0.1:7373"
	cfg.DBPath = ""
	rawConfig, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(config.DefaultPath(), rawConfig, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	previousLauncher := launchBrowser
	var openedURL string
	launchBrowser = func(rawURL string) error {
		openedURL = rawURL
		return nil
	}
	t.Cleanup(func() {
		launchBrowser = previousLauncher
	})

	command := newWebCmd()
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := command.Execute(); err != nil {
		t.Fatalf("execute web: %v", err)
	}
	if openedURL != "http://127.0.0.1:7373/login#single-use-code" {
		t.Fatalf("opened URL = %q", openedURL)
	}
	if strings.Contains(stdout.String(), "single-use-code") {
		t.Fatalf("stdout exposed login code: %q", stdout.String())
	}
	if stdout.String() != "opened Drove console at http://127.0.0.1:7373/login\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestWebCommandRejectsDisabledTCPBeforeDaemonAccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := config.Defaults()
	cfg.DisableTCP = true
	rawConfig, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(config.DefaultPath()), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(config.DefaultPath(), rawConfig, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	err = newWebCmd().Execute()
	if err == nil || !strings.Contains(err.Error(), "disable_tcp") {
		t.Fatalf("web command error = %v, want disable_tcp error", err)
	}
}

func TestBrowserLoginURLPreservesIPv6Authority(t *testing.T) {
	browserURL, err := newBrowserURL("[::1]:7373", "code")
	if err != nil {
		t.Fatalf("browser login URL: %v", err)
	}
	if browserURL.login != "http://[::1]:7373/login#code" ||
		browserURL.public != "http://[::1]:7373/login" {
		t.Fatalf("browser URL = %+v", browserURL)
	}
}

func TestWriteLogRowsPlainStripsControlsAcrossChunks(t *testing.T) {
	first, err := event.HydrateOutputChunkPayload(
		`{"version":1,"offset":0,"len":8}`,
		[]byte("before\x1b]"),
	)
	if err != nil {
		t.Fatalf("hydrate first chunk: %v", err)
	}
	secondData := []byte("0;secret\x1b\\after\x1b[31")
	second, err := event.HydrateOutputChunkPayload(
		fmt.Sprintf(`{"version":1,"offset":8,"len":%d}`, len(secondData)),
		secondData,
	)
	if err != nil {
		t.Fatalf("hydrate second chunk: %v", err)
	}
	thirdData := []byte("m red\x1b[0m")
	third, err := event.HydrateOutputChunkPayload(
		fmt.Sprintf(
			`{"version":1,"offset":%d,"len":%d}`,
			8+len(secondData),
			len(thirdData),
		),
		thirdData,
	)
	if err != nil {
		t.Fatalf("hydrate third chunk: %v", err)
	}

	rows := []store.EventRow{
		{Seq: 1, Type: string(event.TypeOutputChunk), Payload: first},
		{Seq: 2, Type: string(event.TypeOutputChunk), Payload: second},
		{Seq: 3, Type: string(event.TypeOutputChunk), Payload: third},
		{Seq: 4, Type: string(event.TypeOutput), Payload: "legacy\r\n"},
	}
	var output bytes.Buffer
	if err := writeLogRows(&output, rows, true); err != nil {
		t.Fatalf("write plain log rows: %v", err)
	}
	if got, want := output.String(), "beforeafter redlegacy\n"; got != want {
		t.Fatalf("plain output = %q, want %q", got, want)
	}
}
