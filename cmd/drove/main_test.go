package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
			req := sessionStartRequest(test.arg, "name", "/tmp", test.oneshot)
			if req.Vendor != test.wantVendor ||
				req.Command != test.wantCommand ||
				req.Mode != test.wantMode ||
				req.Name != "name" ||
				req.Dir != "/tmp" {
				t.Fatalf("request = %+v", req)
			}
		})
	}
}

func TestUpCommandExposesOneshotFlag(t *testing.T) {
	flag := newUpCmd().Flags().Lookup("oneshot")
	if flag == nil {
		t.Fatal("up command has no --oneshot flag")
	}
	if flag.DefValue != "false" {
		t.Fatalf("--oneshot default = %q, want false", flag.DefValue)
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
	t.Setenv(session.SignalVendorEnv, "claude")
	payload := `{"hook_event_name":"SessionStart","session_id":"vendor-session"}`

	command := newHookCmd()
	command.SetArgs([]string{"--vendor", "claude"})
	command.SetIn(strings.NewReader(payload))
	if err := command.Execute(); err != nil {
		t.Fatalf("execute hook: %v", err)
	}
	if received.Version != 1 ||
		received.Vendor != "claude" ||
		received.DeliveryID == "" ||
		string(received.Payload) != payload {
		t.Fatalf("request = %+v", received)
	}
}

func TestHookCommandValidatesVendorAndInjectedEnvironment(t *testing.T) {
	tests := []struct {
		name           string
		flagVendor     string
		injectedVendor string
		signalURL      string
		want           string
	}{
		{
			name:       "unknown vendor",
			flagVendor: "other",
			want:       "must be claude or codex",
		},
		{
			name:           "vendor mismatch",
			flagVendor:     "codex",
			injectedVendor: "claude",
			signalURL:      "http://127.0.0.1:7373/api/v1/agents/agent-1/signal",
			want:           "does not match",
		},
		{
			name:           "URL agent mismatch",
			flagVendor:     "claude",
			injectedVendor: "claude",
			signalURL:      "http://127.0.0.1:7373/api/v1/agents/agent-2/signal",
			want:           "does not match agent",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(session.SignalAgentIDEnv, "agent-1")
			t.Setenv(session.SignalURLEnv, test.signalURL)
			t.Setenv(session.SignalTokenEnv, "session-token")
			t.Setenv(session.SignalVendorEnv, test.injectedVendor)
			command := newHookCmd()
			command.SetArgs([]string{"--vendor", test.flagVendor})
			command.SetIn(strings.NewReader(`{}`))
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("hook error = %v, want %q", err, test.want)
			}
		})
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

func TestWriteLogRowsUsesLocalTimeAndOneOutputLine(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("test-local", 8*60*60)
	t.Cleanup(func() {
		time.Local = previousLocal
	})

	rows := []store.EventRow{
		{
			Timestamp: time.Date(2026, time.October, 3, 1, 2, 3, 4_000_000, time.UTC),
			Type:      string(event.TypeOutput),
			AgentID:   "12345678-abcd",
			Payload:   "first line\r\n",
		},
		{
			Timestamp: time.Date(2026, time.October, 3, 1, 2, 4, 5_000_000, time.UTC),
			Type:      string(event.TypeStateChanged),
			AgentID:   "12345678-abcd",
			Reason:    "ready",
		},
	}

	var output bytes.Buffer
	if err := writeLogRows(&output, rows); err != nil {
		t.Fatalf("write log rows: %v", err)
	}
	want := "" +
		"09:02:03.004 [12345678]     first line\n" +
		"09:02:04.005 [12345678]     state_changed: ready\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}
