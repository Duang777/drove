// Package localipc provides private HTTP transport between local Drove
// processes over a Unix domain socket.
package localipc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// Authority is the fixed HTTP authority used over the Unix socket.
	Authority = "drove.local"

	runDirectory = "run"
	socketName   = "droved.sock"
	lockName     = "droved.lock"
)

var (
	// ErrAlreadyRunning indicates that another daemon owns the local endpoint.
	ErrAlreadyRunning = errors.New("localipc: daemon already running")
)

// SocketPath returns the daemon Unix socket path for dataDir.
func SocketPath(dataDir string) string {
	return filepath.Join(dataDir, runDirectory, socketName)
}

// Listen creates the private daemon Unix listener.
func Listen(dataDir string) (net.Listener, error) {
	if dataDir == "" {
		return nil, errors.New("localipc: data directory must not be empty")
	}
	absoluteDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("localipc: resolve data directory: %w", err)
	}
	if err := os.MkdirAll(absoluteDataDir, 0o700); err != nil {
		return nil, fmt.Errorf("localipc: create data directory: %w", err)
	}
	runDir := filepath.Join(absoluteDataDir, runDirectory)
	if err := ensurePrivateRunDirectory(runDir); err != nil {
		return nil, err
	}

	lock, err := acquireProcessLock(filepath.Join(runDir, lockName))
	if err != nil {
		return nil, err
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = releaseProcessLock(lock)
		}
	}()

	socketPath := filepath.Join(runDir, socketName)
	if err := removeStaleSocket(socketPath); err != nil {
		return nil, err
	}
	unixListener, err := net.ListenUnix("unix", &net.UnixAddr{
		Name: socketPath,
		Net:  "unix",
	})
	if err != nil {
		return nil, fmt.Errorf("localipc: listen %q: %w", socketPath, err)
	}
	unixListener.SetUnlinkOnClose(true)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = unixListener.Close()
		return nil, fmt.Errorf("localipc: secure socket: %w", err)
	}

	keepLock = true
	return &lockedListener{
		Listener: &peerListener{
			Listener: unixListener,
			uid:      uint32(os.Getuid()),
			peerUID:  peerUserID,
		},
		lock: lock,
	}, nil
}

// Transport returns an HTTP transport that dials dataDir's Unix socket.
func Transport(dataDir string) *http.Transport {
	return TransportSocket(SocketPath(dataDir))
}

// TransportSocket returns an HTTP transport that dials socketPath.
func TransportSocket(socketPath string) *http.Transport {
	dialer := &net.Dialer{}
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableCompression: true,
	}
}

func ensurePrivateRunDirectory(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("localipc: create run directory: %w", err)
		}
		info, err = os.Lstat(path)
	case err != nil:
		return fmt.Errorf("localipc: inspect run directory: %w", err)
	}
	if err != nil {
		return fmt.Errorf("localipc: inspect created run directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("localipc: run path is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf(
			"localipc: run directory permissions are %04o, want 0700",
			info.Mode().Perm(),
		)
	}
	return nil
}

func acquireProcessLock(path string) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("localipc: open process lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), path)
	info, err := lock.Stat()
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("localipc: inspect process lock: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = lock.Close()
		return nil, errors.New("localipc: process lock is not a private regular file")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("localipc: acquire process lock: %w", err)
	}
	return lock, nil
}

func releaseProcessLock(lock *os.File) error {
	if lock == nil {
		return nil
	}
	return errors.Join(
		unix.Flock(int(lock.Fd()), unix.LOCK_UN),
		lock.Close(),
	)
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("localipc: inspect socket: %w", err)
	}
	if info.Mode()&fs.ModeSocket == 0 {
		return errors.New("localipc: socket path exists and is not a socket")
	}
	connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return ErrAlreadyRunning
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("localipc: remove stale socket: %w", err)
	}
	return nil
}

type peerListener struct {
	net.Listener
	uid     uint32
	peerUID func(net.Conn) (uint32, error)
}

func (l *peerListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uid, err := l.peerUID(connection)
		if err == nil && uid == l.uid {
			return connection, nil
		}
		_ = connection.Close()
	}
}

type lockedListener struct {
	net.Listener
	lock      *os.File
	closeOnce sync.Once
	closeErr  error
}

func (l *lockedListener) Close() error {
	l.closeOnce.Do(func() {
		l.closeErr = errors.Join(l.Listener.Close(), releaseProcessLock(l.lock))
	})
	return l.closeErr
}
