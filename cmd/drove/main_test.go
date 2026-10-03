package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
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
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := sessionStartRequest(
				test.arg,
				"name",
				"/tmp",
				test.oneshot,
				test.hooks,
			)
			if req.Vendor != test.wantVendor ||
				req.Command != test.wantCommand ||
				req.Mode != test.wantMode ||
				req.Name != "name" ||
				req.Dir != "/tmp" ||
				req.Hooks != test.hooks {
				t.Fatalf("request = %+v", req)
			}
		})
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
}

func TestUpCommandRejectsInvalidHookPolicyBeforeClientSetup(t *testing.T) {
	command := newUpCmd()
	command.SetArgs([]string{"claude", "--hooks", "sometimes"})
	err := command.Execute()
	if !errors.Is(err, session.ErrInvalidHookPolicy) {
		t.Fatalf("up error = %v, want ErrInvalidHookPolicy", err)
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
