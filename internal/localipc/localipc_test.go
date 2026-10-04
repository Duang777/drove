package localipc

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestListenServesHTTPOverPrivateSocket(t *testing.T) {
	dataDir := shortDataDir(t)
	listener, err := Listen(dataDir)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != Authority {
			t.Errorf("host = %q, want %q", r.Host, Authority)
		}
		_, _ = io.WriteString(w, "ready")
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	client := &http.Client{Transport: Transport(dataDir), Timeout: time.Second}
	response, err := client.Get("http://" + Authority + "/ready")
	if err != nil {
		t.Fatalf("get over Unix socket: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ready" {
		t.Fatalf("response = %s %q", response.Status, body)
	}

	runInfo, err := os.Lstat(filepath.Join(dataDir, runDirectory))
	if err != nil {
		t.Fatalf("inspect run directory: %v", err)
	}
	if runInfo.Mode().Perm() != 0o700 {
		t.Fatalf("run directory mode = %04o, want 0700", runInfo.Mode().Perm())
	}
	socketInfo, err := os.Lstat(SocketPath(dataDir))
	if err != nil {
		t.Fatalf("inspect socket: %v", err)
	}
	if socketInfo.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %04o, want 0600", socketInfo.Mode().Perm())
	}
}

func TestListenRemovesStaleSocketButRejectsOtherFiles(t *testing.T) {
	t.Run("stale socket", func(t *testing.T) {
		dataDir := shortDataDir(t)
		runDir := filepath.Join(dataDir, runDirectory)
		if err := os.Mkdir(runDir, 0o700); err != nil {
			t.Fatalf("create run directory: %v", err)
		}
		stale, err := net.Listen("unix", SocketPath(dataDir))
		if err != nil {
			t.Fatalf("create stale socket: %v", err)
		}
		stale.(*net.UnixListener).SetUnlinkOnClose(false)
		if err := stale.Close(); err != nil {
			t.Fatalf("close stale socket: %v", err)
		}

		listener, err := Listen(dataDir)
		if err != nil {
			t.Fatalf("listen over stale socket: %v", err)
		}
		_ = listener.Close()
	})

	t.Run("regular file", func(t *testing.T) {
		dataDir := shortDataDir(t)
		runDir := filepath.Join(dataDir, runDirectory)
		if err := os.Mkdir(runDir, 0o700); err != nil {
			t.Fatalf("create run directory: %v", err)
		}
		if err := os.WriteFile(SocketPath(dataDir), []byte("not a socket"), 0o600); err != nil {
			t.Fatalf("write socket path: %v", err)
		}
		if _, err := Listen(dataDir); err == nil || !strings.Contains(err.Error(), "not a socket") {
			t.Fatalf("listen error = %v, want file type rejection", err)
		}
	})
}

func TestListenRejectsSecondDaemon(t *testing.T) {
	dataDir := shortDataDir(t)
	first, err := Listen(dataDir)
	if err != nil {
		t.Fatalf("first listen: %v", err)
	}
	defer first.Close()

	if _, err := Listen(dataDir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second listen error = %v, want ErrAlreadyRunning", err)
	}
}

func TestPeerListenerDropsForeignUID(t *testing.T) {
	firstServer, firstClient := net.Pipe()
	secondServer, secondClient := net.Pipe()
	defer firstClient.Close()
	defer secondClient.Close()

	base := &sequenceListener{
		connections: []net.Conn{firstServer, secondServer},
	}
	var calls atomic.Int32
	listener := &peerListener{
		Listener: base,
		uid:      42,
		peerUID: func(net.Conn) (uint32, error) {
			if calls.Add(1) == 1 {
				return 7, nil
			}
			return 42, nil
		},
	}

	accepted, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer accepted.Close()
	if accepted != secondServer {
		t.Fatal("accepted foreign-UID connection")
	}
	if _, err := firstClient.Write([]byte("closed")); err == nil {
		t.Fatal("foreign-UID connection remained open")
	}
}

func TestTransportHonorsRequestCancellation(t *testing.T) {
	dataDir := shortDataDir(t)
	client := &http.Client{Transport: Transport(dataDir)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://"+Authority+"/",
		nil,
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := client.Do(request); !errors.Is(err, context.Canceled) {
		t.Fatalf("request error = %v, want context cancellation", err)
	}
}

type sequenceListener struct {
	connections []net.Conn
}

func (l *sequenceListener) Accept() (net.Conn, error) {
	if len(l.connections) == 0 {
		return nil, net.ErrClosed
	}
	connection := l.connections[0]
	l.connections = l.connections[1:]
	return connection, nil
}

func (*sequenceListener) Close() error {
	return nil
}

func (*sequenceListener) Addr() net.Addr {
	return testAddr("sequence")
}

type testAddr string

func (a testAddr) Network() string {
	return string(a)
}

func (a testAddr) String() string {
	return string(a)
}

func shortDataDir(t *testing.T) string {
	t.Helper()
	dataDir, err := os.MkdirTemp("/tmp", "dli-")
	if err != nil {
		t.Fatalf("create short data directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dataDir); err != nil {
			t.Errorf("remove short data directory: %v", err)
		}
	})
	return dataDir
}
