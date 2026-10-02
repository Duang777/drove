package daemon

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/config"
	"github.com/Duang777/drove/internal/event"
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
		DataDir:     dataDir,
		DBPath:      dbPath,
		APIBind:     occupied.Addr().String(),
		EventBuffer: 1,
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
	if got := recovered.Hub.NextSeq(); got != 42 {
		t.Fatalf("next seq = %d, want 42", got)
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
	if got := recovered.Hub.NextSeq(); got != 1 {
		t.Fatalf("next seq = %d, want 1", got)
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
