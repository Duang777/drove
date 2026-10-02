package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Duang777/drove/internal/adapter"
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/store"
)

func TestStartDoesNotLaunchProcessWhenCreationCannotPersist(t *testing.T) {
	manager, st := newTestManager(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "process-started")
	status, err := manager.Start(context.Background(), StartRequest{
		Name:    "must-not-start",
		Command: "/usr/bin/touch",
		Args:    []string{marker},
	})
	if err == nil {
		t.Fatal("start succeeded with a closed store")
	}
	if !strings.Contains(err.Error(), "persist creation") {
		t.Fatalf("error = %q, want creation persistence context", err)
	}
	if status != nil {
		t.Fatalf("start status = %+v, want nil", status)
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("manager retained %d agents, want 0", len(got))
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("process marker error = %v, want not exist", statErr)
	}
}

func TestStartPreservesFailedPTYStartupHistory(t *testing.T) {
	manager, st := newTestManager(t)

	status, startErr := manager.Start(context.Background(), StartRequest{
		Name:    "broken-agent",
		Command: filepath.Join(t.TempDir(), "missing-agent"),
	})
	if startErr == nil {
		t.Fatal("start succeeded with a missing executable")
	}
	if status != nil {
		t.Fatalf("start status = %+v, want nil", status)
	}

	var rows []store.EventRow
	_, err := st.ScanEvents(context.Background(), func(row store.EventRow) error {
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		t.Fatalf("scan events: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("event count = %d, want 4: %+v", len(rows), rows)
	}
	if rows[0].Seq != 1 ||
		rows[0].Type != string(event.TypeSessionLifecycle) ||
		rows[0].Reason != "created" ||
		rows[0].SessionID != rows[0].AgentID ||
		rows[0].Payload != `{"version":1,"name":"broken-agent","vendor":"generic"}` {
		t.Fatalf("creation event = %+v", rows[0])
	}
	if rows[1].Seq != 2 ||
		rows[1].Type != string(event.TypeStateChanged) ||
		rows[1].From != "pending" ||
		rows[1].To != "starting" {
		t.Fatalf("starting event = %+v", rows[1])
	}
	if rows[2].Seq != 3 || rows[2].Type != string(event.TypeError) || rows[2].Payload == "" {
		t.Fatalf("startup error event = %+v", rows[2])
	}
	if rows[3].Seq != 4 ||
		rows[3].Type != string(event.TypeStateChanged) ||
		rows[3].From != "starting" ||
		rows[3].To != "stopped" {
		t.Fatalf("stopped event = %+v", rows[3])
	}

	failedStatus, err := manager.Status(agent.ID(rows[0].SessionID))
	if err != nil {
		t.Fatalf("status of failed session: %v", err)
	}
	if failedStatus.State != agent.StateStopped {
		t.Fatalf("failed session state = %s, want stopped", failedStatus.State)
	}
	if failedStatus.LastError == "" || failedStatus.LastError != rows[2].Payload {
		t.Fatalf("failed session error = %q, event payload = %q", failedStatus.LastError, rows[2].Payload)
	}
}

func TestStartRejectsInvalidGenericRequestWithoutHistory(t *testing.T) {
	manager, st := newTestManager(t)

	status, err := manager.Start(context.Background(), StartRequest{})
	if err == nil {
		t.Fatal("start succeeded without a generic command")
	}
	if status != nil {
		t.Fatalf("start status = %+v, want nil", status)
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("manager retained %d agents, want 0", len(got))
	}

	lastSeq, err := st.ScanEvents(context.Background(), func(store.EventRow) error { return nil })
	if err != nil {
		t.Fatalf("scan events: %v", err)
	}
	if lastSeq != 0 {
		t.Fatalf("last seq = %d, want 0", lastSeq)
	}
}

func newTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return NewManager(adapter.NewRegistry(), event.NewHub(0), st), st
}
