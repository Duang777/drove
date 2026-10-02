package daemon

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestNewHubFromStoreContinuesPersistedSequence(t *testing.T) {
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

	hub, err := newHubFromStore(st)
	if err != nil {
		t.Fatalf("new hub from store: %v", err)
	}
	if got := hub.NextSeq(); got != 42 {
		t.Fatalf("next seq = %d, want 42", got)
	}
}

func TestNewHubFromStoreStartsEmptyDatabaseAtOne(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	hub, err := newHubFromStore(st)
	if err != nil {
		t.Fatalf("new hub from store: %v", err)
	}
	if got := hub.NextSeq(); got != 1 {
		t.Fatalf("next seq = %d, want 1", got)
	}
}

func TestNewHubFromStoreReturnsSequenceReadError(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if closeErr := st.Close(); closeErr != nil {
		t.Fatalf("close store: %v", closeErr)
	}

	_, err = newHubFromStore(st)
	if err == nil {
		t.Fatal("new hub from closed store returned nil error")
	}
	if !strings.Contains(err.Error(), "daemon: restore event sequence: store: last seq:") {
		t.Fatalf("error = %q, want daemon and store context", err)
	}
}
