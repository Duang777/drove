package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
)

const signalInjectionDirectory = "sessions"

// SignalInjectionOptions configures process-local vendor signal injection.
type SignalInjectionOptions struct {
	DataDir    string
	RelayPath  string
	SocketPath string
	Modes      map[string]agent.SignalInjectionMode
}

type signalInjectionFS struct {
	mkdirAll   func(string, fs.FileMode) error
	createTemp func(string, string) (signalInjectionFile, error)
	rename     func(string, string) error
	removeAll  func(string) error
	lstat      func(string) (fs.FileInfo, error)
	readDir    func(string) ([]os.DirEntry, error)
	open       func(string) (signalInjectionFile, error)
	chmod      func(string, fs.FileMode) error
}

type signalInjectionFile interface {
	Write([]byte) (int, error)
	Chmod(fs.FileMode) error
	Sync() error
	Close() error
	Name() string
}

type injectionResult struct {
	mode   agent.SignalInjectionMode
	status agent.SignalInjectionStatus
	reason agent.SignalInjectionReason
	args   []string
	dir    string
}

// WithSignalInjection configures session-only vendor signal injection.
func WithSignalInjection(options SignalInjectionOptions) ManagerOption {
	modes := make(map[string]agent.SignalInjectionMode, len(options.Modes))
	for vendor, mode := range options.Modes {
		modes[vendor] = mode
	}
	return func(manager *Manager) {
		manager.injectionEnabled = true
		manager.injectionDataDir = options.DataDir
		manager.injectionRelay = options.RelayPath
		manager.injectionModes = modes
		manager.signalSocketPath = options.SocketPath
	}
}

func defaultSignalInjectionFS() signalInjectionFS {
	return signalInjectionFS{
		mkdirAll: os.MkdirAll,
		createTemp: func(directory, pattern string) (signalInjectionFile, error) {
			return os.CreateTemp(directory, pattern)
		},
		rename:    os.Rename,
		removeAll: os.RemoveAll,
		lstat:     os.Lstat,
		readDir:   os.ReadDir,
		open: func(path string) (signalInjectionFile, error) {
			return os.Open(path)
		},
		chmod: os.Chmod,
	}
}

func (m *Manager) prepareSignalInjection(
	id agent.ID,
	entry adapter.Entry,
	vendor string,
	hookPolicy agent.HookPolicy,
	mode agent.RunMode,
	baseArgs []string,
	requestArgs []string,
) (injectionResult, error) {
	result := injectionResult{
		mode:   agent.SignalInjectionOff,
		status: agent.InjectionOff,
		reason: agent.InjectionReasonConfiguredOff,
		args:   append(append([]string(nil), baseArgs...), requestArgs...),
	}
	configured, explicit := m.injectionModes[vendor]
	switch {
	case !m.injectionEnabled:
		result.mode = agent.SignalInjectionOff
	case explicit:
		if !agent.ValidSignalInjectionMode(configured) {
			return injectionResult{}, fmt.Errorf(
				"session: invalid signal injection mode %q for %q",
				configured,
				vendor,
			)
		}
		result.mode = configured
	case entry.SupportsSignalInjection():
		result.mode = agent.SignalInjectionAuto
	default:
		result.mode = agent.SignalInjectionOff
	}
	if hookPolicy == agent.HooksOff {
		result.status = agent.InjectionOff
		result.reason = agent.InjectionReasonHookPolicyOff
		return result, nil
	}
	if result.mode == agent.SignalInjectionOff {
		return result, nil
	}
	if !entry.SupportsSignalInjection() {
		result.status = agent.InjectionSkipped
		result.reason = agent.InjectionReasonUnsupported
		return result, nil
	}
	if m.injectionRelay == "" {
		result.status = agent.InjectionSkipped
		result.reason = agent.InjectionReasonRelayUnavailable
		return result, nil
	}
	if m.injectionDataDir == "" {
		return injectionResult{}, errors.New(
			"session: signal injection data directory is required",
		)
	}

	dataDir, err := filepath.Abs(m.injectionDataDir)
	if err != nil {
		return injectionResult{}, fmt.Errorf(
			"session: resolve signal injection data directory: %w",
			err,
		)
	}
	sessionDir := filepath.Join(dataDir, signalInjectionDirectory, string(id))
	plan, err := entry.InjectSignals(adapter.SignalInjectionRequest{
		Mode:        mode,
		BaseArgs:    append([]string(nil), baseArgs...),
		RequestArgs: append([]string(nil), requestArgs...),
		RelayPath:   m.injectionRelay,
		SessionDir:  sessionDir,
	})
	if errors.Is(err, adapter.ErrSignalInjectionConflict) {
		result.status = agent.InjectionSkipped
		result.reason = agent.InjectionReasonArgumentConflict
		return result, nil
	}
	if err != nil {
		return injectionResult{}, fmt.Errorf(
			"session: plan signal injection for %q: %w",
			vendor,
			err,
		)
	}
	if len(plan.Files) > 0 {
		if err := m.materializeSignalInjection(sessionDir, plan.Files); err != nil {
			return injectionResult{}, err
		}
		result.dir = sessionDir
	}
	result.status = agent.InjectionInjected
	result.reason = agent.InjectionReasonSessionConfig
	result.args = append([]string(nil), plan.Args...)
	return result, nil
}

func (m *Manager) materializeSignalInjection(
	sessionDir string,
	files []adapter.SignalInjectionFile,
) (returnErr error) {
	root := filepath.Dir(sessionDir)
	if info, err := m.injectionFS.lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("session: signal injection root is not a directory")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := m.injectionFS.mkdirAll(root, 0o700); err != nil {
			return fmt.Errorf("session: create signal injection root: %w", err)
		}
		if err := m.injectionFS.chmod(root, 0o700); err != nil {
			return fmt.Errorf("session: secure signal injection root: %w", err)
		}
	} else {
		return fmt.Errorf("session: inspect signal injection root: %w", err)
	}
	if info, err := m.injectionFS.lstat(sessionDir); err == nil {
		return fmt.Errorf(
			"session: signal injection directory already exists with mode %s",
			info.Mode(),
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("session: inspect signal injection directory: %w", err)
	}
	if err := m.injectionFS.mkdirAll(sessionDir, 0o700); err != nil {
		return fmt.Errorf("session: create signal injection directory: %w", err)
	}
	if err := m.injectionFS.chmod(sessionDir, 0o700); err != nil {
		_ = m.injectionFS.removeAll(sessionDir)
		return fmt.Errorf("session: secure signal injection directory: %w", err)
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, m.injectionFS.removeAll(sessionDir))
		}
	}()

	for _, planned := range files {
		if planned.Path == "" ||
			filepath.IsAbs(planned.Path) ||
			filepath.Clean(planned.Path) != planned.Path ||
			filepath.Base(planned.Path) != planned.Path ||
			planned.Mode != 0o600 {
			return fmt.Errorf(
				"session: invalid signal injection file %q",
				planned.Path,
			)
		}
		target := filepath.Join(sessionDir, planned.Path)
		file, err := m.injectionFS.createTemp(sessionDir, ".drove-injection-*")
		if err != nil {
			return fmt.Errorf("session: create signal injection temporary file: %w", err)
		}
		tempPath := file.Name()
		removeTemp := true
		defer func() {
			if removeTemp {
				_ = os.Remove(tempPath)
			}
		}()
		if err := file.Chmod(planned.Mode); err != nil {
			_ = file.Close()
			return fmt.Errorf("session: set signal injection file mode: %w", err)
		}
		written, err := file.Write(planned.Content)
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("session: write signal injection file: %w", err)
		}
		if written != len(planned.Content) {
			_ = file.Close()
			return fmt.Errorf("session: write signal injection file: %w", io.ErrShortWrite)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("session: sync signal injection file: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("session: close signal injection file: %w", err)
		}
		if err := m.injectionFS.rename(tempPath, target); err != nil {
			return fmt.Errorf("session: publish signal injection file: %w", err)
		}
		removeTemp = false
	}
	if err := syncInjectionDirectory(m.injectionFS, sessionDir); err != nil {
		return err
	}
	return nil
}

func syncInjectionDirectory(files signalInjectionFS, path string) error {
	directory, err := files.open(path)
	if err != nil {
		return fmt.Errorf("session: open signal injection directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("session: sync signal injection directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("session: close signal injection directory: %w", err)
	}
	return nil
}

func (m *Manager) cleanupSignalInjection(id agent.ID, sessionDir string) error {
	if sessionDir == "" {
		return nil
	}
	dataDir, err := filepath.Abs(m.injectionDataDir)
	if err != nil {
		return fmt.Errorf("session: resolve cleanup data directory: %w", err)
	}
	want := filepath.Join(dataDir, signalInjectionDirectory, string(id))
	if filepath.Clean(sessionDir) != want {
		return errors.New("session: signal injection cleanup path mismatch")
	}
	root := filepath.Dir(sessionDir)
	rootInfo, err := m.injectionFS.lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("session: inspect signal injection cleanup root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return errors.New("session: signal injection cleanup root is not a directory")
	}
	info, err := m.injectionFS.lstat(sessionDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("session: inspect signal injection cleanup path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("session: signal injection cleanup path is not a directory")
	}
	if err := m.injectionFS.removeAll(sessionDir); err != nil {
		return fmt.Errorf("session: remove signal injection directory: %w", err)
	}
	return syncInjectionDirectory(m.injectionFS, root)
}

func (m *Manager) finalizeSignalInjection(
	id agent.ID,
	running *runningSession,
) {
	sessionDir := running.injectionDir
	running.injectionDir = ""
	if err := m.cleanupSignalInjection(id, sessionDir); err != nil {
		_, _ = m.committer.CommitEvents(
			context.Background(),
			[]event.Draft{event.NewErrorDraft(
				string(id),
				string(id),
				"signal injection cleanup failed",
			)},
		)
	}
}

// CleanupStaleSignalInjections removes runtime files that cannot survive restart.
func CleanupStaleSignalInjections(dataDir string) error {
	files := defaultSignalInjectionFS()
	root, err := filepath.Abs(filepath.Join(dataDir, signalInjectionDirectory))
	if err != nil {
		return fmt.Errorf("session: resolve stale signal injection root: %w", err)
	}
	info, err := files.lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("session: inspect stale signal injection root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("session: stale signal injection root is not a directory")
	}
	entries, err := files.readDir(root)
	if err != nil {
		return fmt.Errorf("session: list stale signal injections: %w", err)
	}
	changed := false
	for _, entry := range entries {
		parsed, parseErr := uuid.Parse(entry.Name())
		if parseErr != nil || parsed.String() != entry.Name() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		child, err := files.lstat(path)
		if err != nil {
			return fmt.Errorf("session: inspect stale signal injection: %w", err)
		}
		if child.Mode()&os.ModeSymlink != 0 || !child.IsDir() {
			return errors.New("session: stale signal injection is not a directory")
		}
		if err := files.removeAll(path); err != nil {
			return fmt.Errorf("session: remove stale signal injection: %w", err)
		}
		changed = true
	}
	if changed {
		return syncInjectionDirectory(files, root)
	}
	return nil
}
