package clitui

import (
	"reflect"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/session"
)

func TestRowsCopyDisplayFieldsAndStableBlockedFirst(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)
	statuses := []*session.Status{
		newFleetStatus("working-1", agent.StateWorking, now),
		newFleetStatus("blocked-1", agent.StateBlocked, now.Add(time.Second)),
		nil,
		newFleetStatus("blocked-2", agent.StateBlocked, now.Add(2*time.Second)),
		newFleetStatus("done-1", agent.StateDone, now.Add(3*time.Second)),
	}
	statuses[1].Name = "waiting"
	statuses[1].Vendor = "codex"
	statuses[1].HookStatus = detect.HookActive
	statuses[1].LastTransition = &agent.Evidence{
		Source: agent.EvidenceHook,
		Event:  "human_input_required",
	}

	rows := projectFleet(statuses, filterAll)
	wantIDs := []string{"blocked-1", "blocked-2", "working-1", "done-1"}
	if got := rowIDs(rows); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("row IDs = %v, want %v", got, wantIDs)
	}

	got := rows[0]
	want := fleetRow{
		AgentID:          "blocked-1",
		Name:             "waiting",
		Vendor:           "codex",
		State:            agent.StateBlocked,
		HookStatus:       detect.HookActive,
		TransitionEvent:  "human_input_required",
		TransitionSource: agent.EvidenceHook,
		UpdatedAt:        now.Add(time.Second),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("copied row = %+v, want %+v", got, want)
	}

	statuses[1].Name = "changed"
	statuses[1].LastTransition.Event = "changed"
	if rows[0] != got {
		t.Fatalf("row changed through source mutation: got %+v, want %+v", rows[0], got)
	}
}

func TestFilterValuesAndExactMatching(t *testing.T) {
	t.Parallel()

	wantFilters := [...]fleetFilter{
		filterAll,
		filterPending,
		filterStarting,
		filterWorking,
		filterBlocked,
		filterIdle,
		filterDone,
		filterStopped,
	}
	if fleetFilters != wantFilters {
		t.Fatalf("filters = %v, want %v", fleetFilters, wantFilters)
	}

	statuses := []*session.Status{
		newFleetStatus("pending", agent.StatePending, time.Time{}),
		newFleetStatus("starting", agent.StateStarting, time.Time{}),
		newFleetStatus("working", agent.StateWorking, time.Time{}),
		newFleetStatus("blocked", agent.StateBlocked, time.Time{}),
		newFleetStatus("idle", agent.StateIdle, time.Time{}),
		newFleetStatus("done", agent.StateDone, time.Time{}),
		newFleetStatus("stopped", agent.StateStopped, time.Time{}),
	}
	tests := []struct {
		filter fleetFilter
		want   []string
	}{
		{filter: filterAll, want: []string{"blocked", "pending", "starting", "working", "idle", "done", "stopped"}},
		{filter: filterPending, want: []string{"pending"}},
		{filter: filterStarting, want: []string{"starting"}},
		{filter: filterWorking, want: []string{"working"}},
		{filter: filterBlocked, want: []string{"blocked"}},
		{filter: filterIdle, want: []string{"idle"}},
		{filter: filterDone, want: []string{"done"}},
		{filter: filterStopped, want: []string{"stopped"}},
		{filter: fleetFilter("unknown"), want: nil},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.filter), func(t *testing.T) {
			t.Parallel()
			if got := rowIDs(projectFleet(statuses, test.filter)); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("IDs = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSelectionRetainsAgentIDAndClampsFallback(t *testing.T) {
	t.Parallel()

	rows := []fleetRow{
		{AgentID: "agent-c"},
		{AgentID: "agent-a"},
		{AgentID: "agent-b"},
	}
	tests := []struct {
		name    string
		current selection
		want    selection
	}{
		{
			name:    "retains ID after reorder",
			current: selection{AgentID: "agent-b", FallbackIndex: 0},
			want:    selection{AgentID: "agent-b", FallbackIndex: 2},
		},
		{
			name:    "uses fallback",
			current: selection{AgentID: "removed", FallbackIndex: 1},
			want:    selection{AgentID: "agent-a", FallbackIndex: 1},
		},
		{
			name:    "clamps low",
			current: selection{AgentID: "removed", FallbackIndex: -4},
			want:    selection{AgentID: "agent-c", FallbackIndex: 0},
		},
		{
			name:    "clamps high",
			current: selection{AgentID: "removed", FallbackIndex: 99},
			want:    selection{AgentID: "agent-b", FallbackIndex: 2},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := reconcileSelection(test.current, rows); got != test.want {
				t.Fatalf("selection = %+v, want %+v", got, test.want)
			}
		})
	}

	if got := reconcileSelection(selection{AgentID: "removed", FallbackIndex: 2}, nil); got != (selection{}) {
		t.Fatalf("empty selection = %+v, want zero value", got)
	}
}

func TestSelectionNavigationClamps(t *testing.T) {
	t.Parallel()

	rows := []fleetRow{
		{AgentID: "agent-a"},
		{AgentID: "agent-b"},
		{AgentID: "agent-c"},
	}
	tests := []struct {
		name    string
		current selection
		delta   int
		want    selection
	}{
		{
			name:    "moves down",
			current: selection{AgentID: "agent-a"},
			delta:   1,
			want:    selection{AgentID: "agent-b", FallbackIndex: 1},
		},
		{
			name:    "moves up",
			current: selection{AgentID: "agent-c", FallbackIndex: 2},
			delta:   -1,
			want:    selection{AgentID: "agent-b", FallbackIndex: 1},
		},
		{
			name:    "clamps first",
			current: selection{AgentID: "agent-a"},
			delta:   -10,
			want:    selection{AgentID: "agent-a"},
		},
		{
			name:    "clamps last",
			current: selection{AgentID: "agent-c", FallbackIndex: 2},
			delta:   10,
			want:    selection{AgentID: "agent-c", FallbackIndex: 2},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := moveSelection(test.current, rows, test.delta); got != test.want {
				t.Fatalf("selection = %+v, want %+v", got, test.want)
			}
		})
	}

	if got := moveSelection(selection{AgentID: "removed", FallbackIndex: 4}, nil, 1); got != (selection{}) {
		t.Fatalf("empty navigation = %+v, want zero value", got)
	}
}

func newFleetStatus(id string, state agent.State, updatedAt time.Time) *session.Status {
	return &session.Status{
		AgentID:    id,
		Name:       id + "-name",
		Vendor:     "generic",
		State:      state,
		HookStatus: detect.HookOff,
		UpdatedAt:  updatedAt,
	}
}

func rowIDs(rows []fleetRow) []string {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]string, len(rows))
	for index := range rows {
		ids[index] = rows[index].AgentID
	}
	return ids
}
