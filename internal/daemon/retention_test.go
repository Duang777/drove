package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/config"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
)

func TestRunAbortsWhenStartupRetentionFails(t *testing.T) {
	now := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	retentionErr := errors.New("retention unavailable")
	dataDir := t.TempDir()
	daemon := New(&config.Config{
		DataDir:        dataDir,
		DBPath:         filepath.Join(dataDir, "drove.db"),
		APIBind:        reserveAddress(t),
		EventBuffer:    1,
		ConsoleOrigins: []string{"http://localhost:5173"},
		Storage: config.StorageConfig{
			OutputRetentionDays: 7,
		},
	})
	daemon.now = func() time.Time { return now }
	var cutoffs []time.Time
	daemon.pruneOutput = func(
		_ context.Context,
		_ *store.Store,
		cutoff time.Time,
	) (int64, error) {
		cutoffs = append(cutoffs, cutoff)
		return 0, retentionErr
	}

	err := daemon.Run(context.Background())
	if !errors.Is(err, retentionErr) ||
		!strings.Contains(err.Error(), "startup output retention") {
		t.Fatalf("daemon error = %v, want startup retention failure", err)
	}
	if len(cutoffs) != 1 || !cutoffs[0].Equal(now.AddDate(0, 0, -7)) {
		t.Fatalf("retention cutoffs = %v", cutoffs)
	}
}

func TestRetentionLoopUsesTicksAndContinuesAfterFailure(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ticks := make(chan time.Time, 2)
	first := time.Date(2026, time.October, 4, 1, 0, 0, 0, time.UTC)
	second := first.Add(24 * time.Hour)
	calls := make(chan time.Time, 2)
	scheduledErr := errors.New("checkpoint busy")
	var mu sync.Mutex
	callCount := 0
	daemon := New(&config.Config{
		Storage: config.StorageConfig{OutputRetentionDays: 2},
	})
	daemon.retentionTicks = ticks
	daemon.pruneOutput = func(
		_ context.Context,
		_ *store.Store,
		cutoff time.Time,
	) (int64, error) {
		mu.Lock()
		callCount++
		current := callCount
		mu.Unlock()
		calls <- cutoff
		if current == 1 {
			return 0, scheduledErr
		}
		return 3, nil
	}
	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, nil))
	cancel, done := daemon.startRetentionLoop(st, log)
	ticks <- first
	ticks <- second

	for index, want := range []time.Time{
		first.AddDate(0, 0, -2),
		second.AddDate(0, 0, -2),
	} {
		select {
		case got := <-calls:
			if !got.Equal(want) {
				t.Fatalf("cutoff %d = %v, want %v", index, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for retention call %d", index)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retention loop did not stop")
	}
	if !strings.Contains(output.String(), "scheduled output retention failed") ||
		!strings.Contains(output.String(), "scheduled output retention completed") {
		t.Fatalf("retention logs = %q", output.String())
	}
}

func TestRetentionLoopCancellationJoinsActiveCleanup(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ticks := make(chan time.Time, 1)
	started := make(chan struct{})
	daemon := New(&config.Config{
		Storage: config.StorageConfig{OutputRetentionDays: 1},
	})
	daemon.retentionTicks = ticks
	daemon.pruneOutput = func(
		ctx context.Context,
		_ *store.Store,
		_ time.Time,
	) (int64, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	}
	cancel, done := daemon.startRetentionLoop(st, slog.Default())
	ticks <- time.Now().UTC()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("retention cleanup did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retention loop did not join active cleanup")
	}
}

func TestCleanupPreservesProjectionAndNextSequence(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "drove.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	old := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	rows := []store.EventRow{
		{
			Seq:       1,
			Timestamp: old,
			Type:      string(event.TypeStateChanged),
			SessionID: "agent-1",
			AgentID:   "agent-1",
			From:      string(agent.StateWorking),
			To:        string(agent.StateBlocked),
		},
		{
			Seq:              2,
			Timestamp:        old,
			Type:             string(event.TypeOutputChunk),
			SessionID:        "agent-1",
			AgentID:          "agent-1",
			Payload:          `{"version":1,"offset":0,"len":3}`,
			OutputAttachment: []byte("old"),
		},
	}
	if _, err := st.AppendEvents(context.Background(), 0, rows); err != nil {
		t.Fatalf("append fixture: %v", err)
	}
	deleted, err := st.PruneOutputAttachments(
		context.Background(),
		old.Add(24*time.Hour),
	)
	if err != nil {
		t.Fatalf("prune output: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted attachments = %d, want 1", deleted)
	}

	recovered, err := bootstrapSessions(context.Background(), st)
	if err != nil {
		t.Fatalf("bootstrap after cleanup: %v", err)
	}
	defer recovered.Hub.Close()
	defer recovered.Manager.Close()
	status, err := recovered.Manager.Status("agent-1")
	if err != nil {
		t.Fatalf("restored status: %v", err)
	}
	if status.State != agent.StateStopped || status.PID != 0 {
		t.Fatalf("restored status = %+v, want stopped without PID", status)
	}
	recoveredLastSeq := recovered.Hub.LastSeq()
	if recoveredLastSeq <= 2 {
		t.Fatalf("recovered last seq = %d, want reconciliation after seq 2", recoveredLastSeq)
	}

	started, err := recovered.Manager.Start(context.Background(), session.StartRequest{
		Name:    "next-sequence",
		Command: "/bin/sh",
		Args:    []string{"-c", "exit 0"},
		Mode:    agent.RunModeOneshot,
	})
	if err != nil {
		t.Fatalf("start next session: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		replay, replayErr := recovered.Manager.Replay(started.AgentID)
		if replayErr != nil {
			t.Fatalf("replay next session: %v", replayErr)
		}
		if len(replay) > 0 && replay[0].Seq == recoveredLastSeq+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"next session did not continue at seq %d: %+v",
				recoveredLastSeq+1,
				replay,
			)
		}
		time.Sleep(time.Millisecond)
	}
}
