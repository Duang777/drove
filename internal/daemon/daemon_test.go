package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/client"
	"github.com/Duang777/drove/internal/config"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
)

func TestRunBootstrapsBeforeOpeningListener(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "drove.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.AppendEvent(store.EventRow{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Type:      string(event.TypeSessionLifecycle),
		SessionID: "agent-1",
		AgentID:   "agent-1",
		Reason:    "created",
		Payload:   "{",
	}); err != nil {
		t.Fatalf("append corrupt event: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy listener: %v", err)
	}
	defer occupied.Close()

	err = New(&config.Config{
		DataDir:        dataDir,
		DBPath:         dbPath,
		APIBind:        occupied.Addr().String(),
		EventBuffer:    1,
		ConsoleOrigins: []string{"http://localhost:5173"},
	}).Run(context.Background())
	if err == nil {
		t.Fatal("daemon run succeeded with corrupt recovery history")
	}
	if !strings.Contains(err.Error(), "bootstrap") || !strings.Contains(err.Error(), "decode creation metadata") {
		t.Fatalf("error = %q, want bootstrap projection error", err)
	}
	if strings.Contains(err.Error(), "listen") {
		t.Fatalf("error = %q, listener opened before bootstrap finished", err)
	}
}

func TestBootstrapSessionsContinuesPersistedSequence(t *testing.T) {
	// 覆盖支持的单 daemon 重启路径，不模拟并发写同一个数据库。
	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	err = st.AppendEvent(store.EventRow{
		Seq:       41,
		Timestamp: time.Unix(0, 0).UTC(),
		Type:      string(event.TypeOutput),
		SessionID: "session",
	})
	if err != nil {
		t.Fatalf("append event: %v", err)
	}

	recovered, err := bootstrapSessions(context.Background(), st)
	if err != nil {
		t.Fatalf("bootstrap sessions: %v", err)
	}
	if got := recovered.Hub.LastSeq(); got != 41 {
		t.Fatalf("last seq = %d, want 41", got)
	}
	if len(recovered.Manager.List()) != 0 {
		t.Fatal("output-only history restored a session")
	}
}

func TestBootstrapSessionsStartsEmptyDatabaseAtOne(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	recovered, err := bootstrapSessions(context.Background(), st)
	if err != nil {
		t.Fatalf("bootstrap sessions: %v", err)
	}
	if got := recovered.Hub.LastSeq(); got != 0 {
		t.Fatalf("last seq = %d, want 0", got)
	}
}

func TestRunServesLocalClientWithTCPDisabled(t *testing.T) {
	dataDir := shortDaemonDataDir(t)
	controlToken, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure control token: %v", err)
	}
	tcpAddress := reserveAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- New(&config.Config{
			DataDir:        dataDir,
			DBPath:         filepath.Join(dataDir, "drove.db"),
			APIBind:        tcpAddress,
			DisableTCP:     true,
			EventBuffer:    16,
			ConsoleOrigins: []string{"http://localhost:5173"},
		}).Run(ctx)
	}()
	t.Cleanup(cancel)

	localClient := client.NewLocal(
		dataDir,
		client.WithTokenFile(auth.TokenPath(dataDir)),
	)
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := localClient.Ping(context.Background())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("local API did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tokenRead, err := auth.Read(auth.TokenPath(dataDir)); err != nil || tokenRead != controlToken {
		t.Fatalf("control token = %q, error = %v", tokenRead, err)
	}
	if connection, err := net.DialTimeout("tcp", tcpAddress, 100*time.Millisecond); err == nil {
		_ = connection.Close()
		t.Fatal("TCP listener is open while disabled")
	}

	cancel()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("daemon run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

func TestBootstrapSessionsRestoresHistoricalSession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	if err := st.AppendEvent(store.EventRow{
		Seq:       1,
		Timestamp: time.Unix(0, 0).UTC(),
		Type:      string(event.TypeStateChanged),
		SessionID: "agent-1",
		AgentID:   "agent-1",
		From:      "working",
		To:        "done",
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	recovered, err := bootstrapSessions(context.Background(), st)
	if err != nil {
		t.Fatalf("bootstrap sessions: %v", err)
	}
	status, err := recovered.Manager.Status("agent-1")
	if err != nil {
		t.Fatalf("restored status: %v", err)
	}
	if status.State != agent.StateStopped || status.PID != 0 {
		t.Fatalf("restored status = %+v", status)
	}
	if status.HookPolicy != agent.HooksOff ||
		status.HookStatus != detect.HookDetached ||
		status.LastTransition == nil ||
		status.LastTransition.Source != agent.EvidenceRecovery ||
		status.LastTransition.Event != "daemon_restart" {
		t.Fatalf("restored hook status = %+v", status)
	}
	if recovered.Recovery.Sessions != 1 ||
		recovered.Recovery.LegacyMetadata != 1 ||
		recovered.Recovery.LastSeq != 2 {
		t.Fatalf("recovery report = %+v", recovered.Recovery)
	}
}

func TestBootstrapSessionsReturnsScanError(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if closeErr := st.Close(); closeErr != nil {
		t.Fatalf("close store: %v", closeErr)
	}

	_, err = bootstrapSessions(context.Background(), st)
	if err == nil {
		t.Fatal("bootstrap with closed store returned nil error")
	}
	if !strings.Contains(err.Error(), "daemon: bootstrap sessions: session: bootstrap scan: store: scan events:") {
		t.Fatalf("error = %q, want daemon and store context", err)
	}
}

func TestRunStopsLiveSessionBeforeClosingStore(t *testing.T) {
	dataDir := shortDaemonDataDir(t)
	dbPath := filepath.Join(dataDir, "drove.db")
	addr := reserveAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	controlToken, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure control token: %v", err)
	}

	runResult := make(chan error, 1)
	go func() {
		runResult <- New(&config.Config{
			DataDir:        dataDir,
			DBPath:         dbPath,
			APIBind:        addr,
			EventBuffer:    16,
			ConsoleOrigins: []string{"http://localhost:5173"},
		}).Run(ctx)
	}()

	started := startAgentThroughAPI(t, addr, controlToken)
	childNeedsCleanup := true
	t.Cleanup(func() {
		if childNeedsCleanup {
			_ = syscall.Kill(started.PID, syscall.SIGKILL)
		}
	})

	cancel()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("daemon run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}

	if err := syscall.Kill(started.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("agent process %d still exists after daemon shutdown: %v", started.PID, err)
	}
	childNeedsCleanup = false

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()

	lastSeqBeforeRestart, err := st.LastSeq()
	if err != nil {
		t.Fatalf("last seq before restart: %v", err)
	}
	rows, err := st.Replay(started.AgentID)
	if err != nil {
		t.Fatalf("replay after shutdown: %v", err)
	}
	hasStopped := false
	for _, row := range rows {
		if row.Type == string(event.TypeStateChanged) &&
			row.To == string(agent.StateStopped) {
			hasStopped = true
		}
	}
	if !hasStopped {
		t.Fatalf("replay has no stopped event after shutdown: %+v", rows)
	}

	recovered, err := bootstrapSessions(context.Background(), st)
	if err != nil {
		t.Fatalf("bootstrap after clean shutdown: %v", err)
	}
	defer recovered.Hub.Close()
	defer recovered.Manager.Close()

	if recovered.Recovery.Interrupted != 0 {
		t.Fatalf("restart interrupted sessions = %d, want 0", recovered.Recovery.Interrupted)
	}
	if recovered.Recovery.LastSeq != lastSeqBeforeRestart {
		t.Fatalf(
			"restart last seq = %d, want unchanged %d",
			recovered.Recovery.LastSeq,
			lastSeqBeforeRestart,
		)
	}
	status, err := recovered.Manager.Status(agent.ID(started.AgentID))
	if err != nil {
		t.Fatalf("restored status: %v", err)
	}
	if status.State != agent.StateStopped || status.PID != 0 {
		t.Fatalf("restored status = %+v, want stopped without PID", status)
	}
}

func TestRunStopsAfterRuntimeEventCommitFailure(t *testing.T) {
	dataDir := shortDaemonDataDir(t)
	dbPath := filepath.Join(dataDir, "drove.db")
	addr := reserveAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	controlToken, err := auth.Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure control token: %v", err)
	}

	runResult := make(chan error, 1)
	go func() {
		runResult <- New(&config.Config{
			DataDir:        dataDir,
			DBPath:         dbPath,
			APIBind:        addr,
			EventBuffer:    16,
			ConsoleOrigins: []string{"http://localhost:5173"},
		}).Run(ctx)
	}()
	waitForAPI(t, addr, controlToken)

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE events`); err != nil {
		_ = db.Close()
		t.Fatalf("drop events table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	request, err := http.NewRequest(
		http.MethodPost,
		"http://"+addr+"/api/v1/agents",
		strings.NewReader(`{"vendor":"generic","command":"/bin/cat"}`),
	)
	if err != nil {
		t.Fatalf("build start request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+controlToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("start request after storage failure: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("start status = %d, want 500", response.StatusCode)
	}

	select {
	case runErr := <-runResult:
		if runErr == nil ||
			!strings.Contains(runErr.Error(), "session event commit") ||
			!strings.Contains(runErr.Error(), "no such table") {
			t.Fatalf("daemon error = %v, want fatal event commit failure", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon continued running after event commit failure")
	}
}

func TestLoopbackHostsIncludesConfiguredAddressAndAliases(t *testing.T) {
	hosts, err := loopbackHosts("127.0.0.2:7373")
	if err != nil {
		t.Fatalf("loopback hosts: %v", err)
	}
	want := []string{
		"127.0.0.2:7373",
		"127.0.0.1:7373",
		"localhost:7373",
		"[::1]:7373",
	}
	if !slices.Equal(hosts, want) {
		t.Fatalf("hosts = %#v, want %#v", hosts, want)
	}
}

func reserveAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release address: %v", err)
	}
	return addr
}

func shortDaemonDataDir(t *testing.T) string {
	t.Helper()
	dataDir, err := os.MkdirTemp("/tmp", "drove-daemon-")
	if err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dataDir); err != nil {
			t.Errorf("remove data directory: %v", err)
		}
	})
	return dataDir
}

func waitForAPI(t *testing.T, addr, controlToken string) {
	t.Helper()

	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	for {
		request, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/v1/agents", nil)
		if err != nil {
			t.Fatalf("build readiness request: %v", err)
		}
		request.Header.Set("Authorization", "Bearer "+controlToken)
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon API did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startAgentThroughAPI(t *testing.T, addr, controlToken string) *session.Status {
	t.Helper()

	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	for {
		request, err := http.NewRequest(
			http.MethodPost,
			"http://"+addr+"/api/v1/agents",
			strings.NewReader(`{"vendor":"generic","name":"shutdown-agent","command":"/bin/cat"}`),
		)
		if err != nil {
			t.Fatalf("build create request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+controlToken)
		response, err := client.Do(request)
		if err == nil {
			var status session.Status
			decodeErr := json.NewDecoder(response.Body).Decode(&status)
			closeErr := response.Body.Close()
			if response.StatusCode != http.StatusCreated {
				t.Fatalf("create status = %d, body = %+v", response.StatusCode, status)
			}
			if decodeErr != nil {
				t.Fatalf("decode create response: %v", decodeErr)
			}
			if closeErr != nil {
				t.Fatalf("close create response: %v", closeErr)
			}
			if status.PID <= 0 {
				t.Fatalf("created status = %+v, want live PID", status)
			}
			return &status
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon API did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
