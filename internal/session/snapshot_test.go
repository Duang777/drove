package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
)

func TestSnapshotWatchCarriesCursorAndCoalescesLatest(t *testing.T) {
	manager, id, running, clock := newSnapshotTestRuntime(t)

	first := []byte("first")
	if err := running.output.Feed(first, 0); err != nil {
		t.Fatalf("feed first output: %v", err)
	}
	clock.timerAt(t, 0).fire(clock.Now().Add(terminalSampleWait))
	waitForSnapshotOffset(t, running.terminal, uint64(len(first)))

	attachment, err := manager.AttachTerminal(
		context.Background(),
		id,
		AttachmentOptions{Mode: AttachmentReadOnly},
	)
	if err != nil {
		t.Fatalf("attach snapshot reader: %v", err)
	}
	snapshots, err := attachment.WatchSnapshots(context.Background())
	if err != nil {
		t.Fatalf("watch snapshots: %v", err)
	}
	if _, err := attachment.WatchSnapshots(
		context.Background(),
	); !errors.Is(err, ErrSnapshotWatchExists) {
		t.Fatalf("duplicate watch error = %v", err)
	}

	second := []byte("-second")
	if err := running.output.Feed(second, uint64(len(first))); err != nil {
		t.Fatalf("feed second output: %v", err)
	}
	clock.timerAt(t, 0).fire(clock.Now().Add(terminalSampleWait))
	wantOffset := uint64(len(first) + len(second))
	waitForSnapshotOffset(t, running.terminal, wantOffset)

	snapshotTimer := clock.timerAt(t, 1)
	if delay, resets := snapshotTimer.state(); delay != liveSnapshotInterval || resets != 0 {
		t.Fatalf(
			"snapshot timer = (%s, %d resets), want (%s, 0)",
			delay,
			resets,
			liveSnapshotInterval,
		)
	}
	snapshotTimer.fire(clock.Now().Add(liveSnapshotInterval))
	waitForTimerReset(t, snapshotTimer, 1)

	select {
	case snapshot := <-snapshots:
		if uint64(snapshot.Cursor.NextOffset) != wantOffset {
			t.Fatalf(
				"snapshot next offset = %d, want %d",
				snapshot.Cursor.NextOffset,
				wantOffset,
			)
		}
		if snapshot.Cursor.Seq == 0 {
			t.Fatal("snapshot sequence remained at origin")
		}
		if snapshot.Rows != initialTerminalRows ||
			snapshot.Columns != initialTerminalColumns {
			t.Fatalf(
				"snapshot dimensions = %dx%d",
				snapshot.Rows,
				snapshot.Columns,
			)
		}
		if snapshot.Restorable {
			t.Fatal("live snapshot was marked restorable")
		}
		if len(snapshot.Lines) != 1 || snapshot.Lines[0] != "first-second" {
			t.Fatalf("snapshot lines = %q", snapshot.Lines)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for coalesced snapshot")
	}

	rows, err := manager.Replay(string(id))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("snapshot watch persisted events: %+v", rows)
	}
	if err := attachment.Close(); err != nil {
		t.Fatalf("detach snapshot reader: %v", err)
	}
	select {
	case _, open := <-snapshots:
		if open {
			t.Fatal("snapshot channel remained open after detach")
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot channel did not close after detach")
	}
}

func TestSnapshotWatchClosesAtOutputEnd(t *testing.T) {
	manager, id, running, clock := newSnapshotTestRuntime(t)
	output := []byte("final")
	if err := running.output.Feed(output, 0); err != nil {
		t.Fatalf("feed output: %v", err)
	}
	clock.timerAt(t, 0).fire(clock.Now().Add(terminalSampleWait))
	waitForSnapshotOffset(t, running.terminal, uint64(len(output)))

	attachment, err := manager.AttachTerminal(
		context.Background(),
		id,
		AttachmentOptions{Mode: AttachmentReadOnly},
	)
	if err != nil {
		t.Fatalf("attach snapshot reader: %v", err)
	}
	defer attachment.Close()
	snapshots, err := attachment.WatchSnapshots(context.Background())
	if err != nil {
		t.Fatalf("watch snapshots: %v", err)
	}
	select {
	case <-snapshots:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial snapshot")
	}

	if err := running.output.End(uint64(len(output))); err != nil {
		t.Fatalf("end output: %v", err)
	}
	select {
	case _, open := <-snapshots:
		if open {
			t.Fatal("snapshot channel remained open after output end")
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot channel did not close after output end")
	}
}

func newSnapshotTestRuntime(
	t *testing.T,
) (*Manager, agent.ID, *runningSession, *terminalTestClock) {
	t.Helper()
	manager, _ := newTestManager(t)
	clock := newTerminalTestClock(time.Unix(90, 0).UTC())
	manager.clock = clock
	id := agent.ID("snapshot-agent")
	target := agent.New(
		id,
		agent.WithName("snapshot-agent"),
		agent.WithVendor("generic"),
		agent.WithRunMode(agent.RunModeInteractive),
	)
	process := &terminalTestProcess{}
	running := &runningSession{
		process: process,
		vendor:  "generic",
	}
	running.output = newOutputProcessor(manager, id, running, "", nil)
	running.terminal = newTerminalTestActor(
		t,
		"generic",
		clock,
		process,
		&recordingTerminalObserver{},
	)
	manager.mu.Lock()
	manager.agents[id] = newManagedAgent(target)
	manager.sessions[id] = running
	manager.mu.Unlock()
	return manager, id, running, clock
}

func waitForSnapshotOffset(
	t *testing.T,
	actor *terminalActor,
	want uint64,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		_, _, offset, available := actor.snapshotState()
		if available && offset == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal snapshot offset did not reach %d", want)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForTimerReset(
	t *testing.T,
	timer *terminalTestTimer,
	want int,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		_, resets := timer.state()
		if resets >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timer resets did not reach %d", want)
		}
		time.Sleep(time.Millisecond)
	}
}
