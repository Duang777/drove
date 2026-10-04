package adapter

import (
	"errors"
	"testing"

	"github.com/Duang777/drove/internal/agent"
)

func TestRegistrySignalInjectionCapabilities(t *testing.T) {
	registry := NewRegistry()
	for _, vendor := range []string{"claude", "codex"} {
		if !registry.For(vendor).SupportsSignalInjection() {
			t.Fatalf("%s has no signal injector", vendor)
		}
	}
	if registry.For("generic").SupportsSignalInjection() {
		t.Fatal("generic unexpectedly supports signal injection")
	}
	_, err := registry.For("generic").InjectSignals(SignalInjectionRequest{})
	if !errors.Is(err, ErrUnsupportedSignalInjection) {
		t.Fatalf("generic injection error = %v", err)
	}
}

func TestInjectSignalsValidatesLaunchPaths(t *testing.T) {
	entry := NewRegistry().For("claude")
	request := SignalInjectionRequest{
		Mode:       agent.RunModeInteractive,
		RelayPath:  "drove",
		SessionDir: "/tmp/session",
	}
	if _, err := entry.InjectSignals(request); err == nil {
		t.Fatal("relative relay path was accepted")
	}
	request.RelayPath = "/tmp/drove"
	request.SessionDir = "session"
	if _, err := entry.InjectSignals(request); err == nil {
		t.Fatal("relative session directory was accepted")
	}
}

func TestInjectSignalsRejectsTerminalNotificationsOnHookChannel(t *testing.T) {
	_, err := injectSignals(
		signalInjectorFunc(func(SignalInjectionRequest) (SignalInjectionPlan, error) {
			return SignalInjectionPlan{
				Channel:               SignalChannelHook,
				TerminalNotifications: true,
			}, nil
		}),
		SignalInjectionRequest{
			Mode:       agent.RunModeInteractive,
			RelayPath:  "/tmp/drove",
			SessionDir: "/tmp/session",
		},
	)
	if err == nil {
		t.Fatal("hook plan enabled terminal notifications")
	}
}

func TestShellCommandQuotesEveryArgument(t *testing.T) {
	got := shellCommand(
		"/tmp/drove cli",
		[]string{"hook", "single'quote"},
	)
	want := `'/tmp/drove cli' 'hook' 'single'"'"'quote'`
	if got != want {
		t.Fatalf("shell command = %q, want %q", got, want)
	}
}

type signalInjectorFunc func(SignalInjectionRequest) (SignalInjectionPlan, error)

func (f signalInjectorFunc) InjectSignals(
	request SignalInjectionRequest,
) (SignalInjectionPlan, error) {
	return f(request)
}
