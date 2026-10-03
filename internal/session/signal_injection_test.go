package session

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestClaudeSessionInjectionMaterializesAndCleansPrivateSettings(t *testing.T) {
	dataDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "args")
	command := writeArgumentCaptureCommand(t, capture)
	manager := newInjectionTestManager(t, dataDir, "/bin/echo", nil)

	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: command,
		Args:    []string{"--caller-arg"},
		Hooks:   agent.HooksAuto,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if status.SignalInjection != agent.SignalInjectionAuto ||
		status.SignalInjectionStatus != agent.InjectionInjected ||
		status.SignalInjectionReason != agent.InjectionReasonSessionConfig {
		t.Fatalf("injection status = %+v", status)
	}

	args := waitForCapturedArgs(t, capture)
	settingsPath := filepath.Join(
		dataDir,
		signalInjectionDirectory,
		status.AgentID,
		claudeSettingsName,
	)
	wantArgs := []string{"--settings", settingsPath, "--caller-arg"}
	if strings.Join(args, "\n") != strings.Join(wantArgs, "\n") {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
	info, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat settings: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %o, want 600", info.Mode().Perm())
	}
	content, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(content), `"SessionStart"`) ||
		strings.Contains(string(content), testSignalToken) {
		t.Fatalf("settings content is invalid or contains a token: %s", content)
	}

	if err := manager.Stop(agent.ID(status.AgentID)); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(settingsPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session injection directory remains: %v", err)
	}
}

func TestCodexSessionInjectionChangesOnlyProcessArguments(t *testing.T) {
	dataDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "args")
	command := writeArgumentCaptureCommand(t, capture)
	manager := newInjectionTestManager(t, dataDir, "/bin/echo", nil)

	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "codex",
		Command: command,
		Args:    []string{"--caller-arg"},
		Hooks:   agent.HooksAuto,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	args := waitForCapturedArgs(t, capture)
	if len(args) != 3 ||
		args[0] != "-c" ||
		!strings.HasPrefix(args[1], "notify=[") ||
		args[2] != "--caller-arg" {
		t.Fatalf("args = %#v", args)
	}
	if status.SignalInjectionStatus != agent.InjectionInjected ||
		status.HookStatus != "awaiting_hook" {
		t.Fatalf("status = %+v", status)
	}
	sessionDir := filepath.Join(dataDir, signalInjectionDirectory, status.AgentID)
	if _, err := os.Lstat(sessionDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Codex created a session file directory: %v", err)
	}
	if err := manager.Stop(agent.ID(status.AgentID)); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestSessionInjectionOffAndConflictPreserveCallerArguments(t *testing.T) {
	tests := []struct {
		name       string
		modes      map[string]agent.SignalInjectionMode
		args       []string
		wantStatus agent.SignalInjectionStatus
		wantReason agent.SignalInjectionReason
	}{
		{
			name:       "configured off",
			modes:      map[string]agent.SignalInjectionMode{"claude": agent.SignalInjectionOff},
			args:       []string{"--caller-arg"},
			wantStatus: agent.InjectionOff,
			wantReason: agent.InjectionReasonConfiguredOff,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			capture := filepath.Join(t.TempDir(), "args")
			command := writeArgumentCaptureCommand(t, capture)
			manager := newInjectionTestManager(t, dataDir, "/bin/echo", test.modes)

			status, err := manager.Start(context.Background(), StartRequest{
				Vendor:  "claude",
				Command: command,
				Args:    test.args,
				Hooks:   agent.HooksAuto,
			})
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			if status.SignalInjectionStatus != test.wantStatus ||
				status.SignalInjectionReason != test.wantReason {
				t.Fatalf("status = %+v", status)
			}
			if got := waitForCapturedArgs(t, capture); strings.Join(got, "\n") !=
				strings.Join(test.args, "\n") {
				t.Fatalf("args = %#v, want %#v", got, test.args)
			}
			if err := manager.Stop(agent.ID(status.AgentID)); err != nil {
				t.Fatalf("stop: %v", err)
			}
		})
	}
}

func TestSessionInjectionConflictIsReportedWithoutMaterialization(t *testing.T) {
	manager := newInjectionTestManager(t, t.TempDir(), "/bin/echo", nil)
	result, err := manager.prepareSignalInjection(
		agent.ID("550e8400-e29b-41d4-a716-446655440000"),
		manager.reg.For("claude"),
		"claude",
		agent.HooksAuto,
		agent.RunModeInteractive,
		nil,
		[]string{"--settings=/tmp/caller.json"},
	)
	if err != nil {
		t.Fatalf("prepare injection: %v", err)
	}
	if result.status != agent.InjectionSkipped ||
		result.reason != agent.InjectionReasonArgumentConflict ||
		len(result.args) != 1 ||
		result.args[0] != "--settings=/tmp/caller.json" ||
		result.dir != "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSessionInjectionMaterializationFailurePrecedesCreation(t *testing.T) {
	dataDir := t.TempDir()
	manager := newInjectionTestManager(t, dataDir, "/bin/echo", nil)
	manager.injectionFS.mkdirAll = func(string, os.FileMode) error {
		return errors.New("injected mkdir failure")
	}

	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
		Hooks:   agent.HooksAuto,
	})
	if err == nil || !strings.Contains(err.Error(), "injected mkdir failure") {
		t.Fatalf("start status=%+v error=%v", status, err)
	}
	var events int
	if _, err := manager.store.ScanEvents(context.Background(), func(store.EventRow) error {
		events++
		return nil
	}); err != nil {
		t.Fatalf("scan events: %v", err)
	}
	if events != 0 {
		t.Fatalf("persisted %d events before materialization", events)
	}
}

func TestSignalInjectionFileFailuresRemovePartialDirectory(t *testing.T) {
	tests := []string{"chmod", "write", "sync", "close", "rename", "directory_sync"}
	for _, failure := range tests {
		t.Run(failure, func(t *testing.T) {
			manager := &Manager{injectionFS: defaultSignalInjectionFS()}
			originalCreate := manager.injectionFS.createTemp
			if failure == "rename" {
				manager.injectionFS.rename = func(string, string) error {
					return errors.New("injected rename failure")
				}
			}
			if failure == "directory_sync" {
				manager.injectionFS.open = func(string) (signalInjectionFile, error) {
					return nil, errors.New("injected directory sync failure")
				}
			}
			if failure == "chmod" ||
				failure == "write" ||
				failure == "sync" ||
				failure == "close" {
				manager.injectionFS.createTemp = func(
					directory string,
					pattern string,
				) (signalInjectionFile, error) {
					file, err := originalCreate(directory, pattern)
					if err != nil {
						return nil, err
					}
					return &failingSignalInjectionFile{
						signalInjectionFile: file,
						failure:             failure,
					}, nil
				}
			}

			sessionDir := filepath.Join(
				t.TempDir(),
				signalInjectionDirectory,
				"550e8400-e29b-41d4-a716-446655440000",
			)
			err := manager.materializeSignalInjection(
				sessionDir,
				[]adapter.SignalInjectionFile{{
					Path:    claudeSettingsName,
					Content: []byte("{}\n"),
					Mode:    0o600,
				}},
			)
			if err == nil {
				t.Fatal("materialization succeeded")
			}
			if _, statErr := os.Lstat(sessionDir); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial directory remains: %v", statErr)
			}
		})
	}
}

func TestSessionInjectionCleanupFailureIsRedactedAndAudited(t *testing.T) {
	dataDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "args")
	command := writeArgumentCaptureCommand(t, capture)
	manager := newInjectionTestManager(t, dataDir, "/bin/echo", nil)
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: command,
		Hooks:   agent.HooksAuto,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForCapturedArgs(t, capture)

	manager.injectionFS.removeAll = func(string) error {
		return errors.New("secret cleanup failure")
	}
	if err := manager.Stop(agent.ID(status.AgentID)); err != nil {
		t.Fatalf("stop: %v", err)
	}
	rows, err := manager.Replay(status.AgentID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Type == string(event.TypeError) &&
			row.Payload == "signal injection cleanup failed" {
			found = true
		}
		if strings.Contains(row.Payload, "secret cleanup failure") {
			t.Fatalf("cleanup cause leaked into event: %+v", row)
		}
	}
	if !found {
		t.Fatalf("cleanup error event not found: %+v", rows)
	}
}

func TestCleanupStaleSignalInjectionsLeavesUnknownEntries(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, signalInjectionDirectory)
	staleID := "550e8400-e29b-41d4-a716-446655440000"
	stale := filepath.Join(root, staleID)
	unknown := filepath.Join(root, "keep-me")
	for _, path := range []string{stale, unknown} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	if err := CleanupStaleSignalInjections(dataDir); err != nil {
		t.Fatalf("cleanup stale injections: %v", err)
	}
	if _, err := os.Lstat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale directory remains: %v", err)
	}
	if info, err := os.Stat(unknown); err != nil || !info.IsDir() {
		t.Fatalf("unknown directory changed: info=%v error=%v", info, err)
	}
}

func TestSignalInjectionKeepsExistingRootMode(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, signalInjectionDirectory)
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create signal injection root: %v", err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatalf("set signal injection root mode: %v", err)
	}
	manager := newInjectionTestManager(t, dataDir, "/bin/echo", nil)
	sessionDir := filepath.Join(root, "550e8400-e29b-41d4-a716-446655440000")
	if err := manager.materializeSignalInjection(
		sessionDir,
		[]adapter.SignalInjectionFile{{
			Path:    claudeSettingsName,
			Content: []byte("{}\n"),
			Mode:    0o600,
		}},
	); err != nil {
		t.Fatalf("materialize signal injection: %v", err)
	}

	info, err := os.Lstat(root)
	if err != nil {
		t.Fatalf("inspect signal injection root: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("existing root mode = %04o, want unchanged 0755", info.Mode().Perm())
	}
	info, err = os.Lstat(sessionDir)
	if err != nil {
		t.Fatalf("inspect session injection directory: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("new session directory mode = %04o, want 0700", info.Mode().Perm())
	}
}

func TestSignalInjectionRejectsSymlinkRoot(t *testing.T) {
	dataDir := t.TempDir()
	target := t.TempDir()
	root := filepath.Join(dataDir, signalInjectionDirectory)
	if err := os.Symlink(target, root); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}
	if err := CleanupStaleSignalInjections(dataDir); err == nil {
		t.Fatal("stale cleanup accepted a symlink root")
	}

	manager := newInjectionTestManager(t, dataDir, "/bin/echo", nil)
	status, err := manager.Start(context.Background(), StartRequest{
		Vendor:  "claude",
		Command: "/bin/cat",
		Hooks:   agent.HooksAuto,
	})
	if err == nil {
		t.Fatalf("start status = %+v, want symlink error", status)
	}
}

const claudeSettingsName = "claude-settings.json"

type failingSignalInjectionFile struct {
	signalInjectionFile
	failure string
}

func (f *failingSignalInjectionFile) Chmod(mode os.FileMode) error {
	if f.failure == "chmod" {
		return errors.New("injected chmod failure")
	}
	return f.signalInjectionFile.Chmod(mode)
}

func (f *failingSignalInjectionFile) Write(content []byte) (int, error) {
	if f.failure == "write" {
		return 0, errors.New("injected write failure")
	}
	return f.signalInjectionFile.Write(content)
}

func (f *failingSignalInjectionFile) Sync() error {
	if f.failure == "sync" {
		return errors.New("injected sync failure")
	}
	return f.signalInjectionFile.Sync()
}

func (f *failingSignalInjectionFile) Close() error {
	err := f.signalInjectionFile.Close()
	if f.failure == "close" {
		return errors.Join(err, errors.New("injected close failure"))
	}
	return err
}

func newInjectionTestManager(
	t *testing.T,
	dataDir string,
	relayPath string,
	modes map[string]agent.SignalInjectionMode,
) *Manager {
	t.Helper()
	st := newTestStore(t)
	manager := NewManager(
		adapter.NewRegistry(),
		event.NewHub(0),
		st,
		0,
		WithSignalInjection(SignalInjectionOptions{
			DataDir:   dataDir,
			RelayPath: relayPath,
			Modes:     modes,
		}),
	)
	manager.newCredential = func() (string, signalTokenDigest, error) {
		return testSignalToken,
			signalTokenDigest(sha256.Sum256([]byte(testSignalToken))),
			nil
	}
	origin, err := url.Parse("http://127.0.0.1:7373")
	if err != nil {
		t.Fatalf("parse signal origin: %v", err)
	}
	if err := manager.ConfigureSignalOrigin(origin); err != nil {
		t.Fatalf("configure signal origin: %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	return manager
}

func writeArgumentCaptureCommand(t *testing.T, capture string) string {
	t.Helper()
	command := filepath.Join(t.TempDir(), "capture.sh")
	script := fmt.Sprintf(
		"#!/bin/sh\ntmp=%q.$$\nprintf '%%s\\n' \"$@\" > \"$tmp\"\nmv \"$tmp\" %q\nwhile IFS= read -r line; do :; done\n",
		capture,
		capture,
	)
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatalf("write capture command: %v", err)
	}
	return command
}

func waitForCapturedArgs(t *testing.T, path string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		content, err := os.ReadFile(path)
		if err == nil {
			return strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read captured args: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for captured arguments")
		}
		time.Sleep(time.Millisecond)
	}
}
