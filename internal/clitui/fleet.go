// Package clitui implements the interactive terminal fleet overview.
package clitui

import (
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/session"
)

type fleetFilter string

const (
	filterAll      fleetFilter = "all"
	filterPending  fleetFilter = "pending"
	filterStarting fleetFilter = "starting"
	filterWorking  fleetFilter = "working"
	filterBlocked  fleetFilter = "blocked"
	filterIdle     fleetFilter = "idle"
	filterDone     fleetFilter = "done"
	filterStopped  fleetFilter = "stopped"
)

var fleetFilters = [...]fleetFilter{
	filterAll,
	filterPending,
	filterStarting,
	filterWorking,
	filterBlocked,
	filterIdle,
	filterDone,
	filterStopped,
}

type fleetRow struct {
	AgentID          string
	Name             string
	Vendor           string
	State            agent.State
	PID              int
	HookStatus       detect.HookStatus
	TransitionEvent  string
	TransitionSource agent.EvidenceSource
	UpdatedAt        time.Time
}

type selection struct {
	AgentID       string
	FallbackIndex int
}

func projectFleet(statuses []*session.Status, filter fleetFilter) []fleetRow {
	blocked := make([]fleetRow, 0, len(statuses))
	other := make([]fleetRow, 0, len(statuses))
	for _, status := range statuses {
		if status == nil || !filter.matches(status.State) {
			continue
		}

		row := fleetRow{
			AgentID:    status.AgentID,
			Name:       status.Name,
			Vendor:     status.Vendor,
			State:      status.State,
			PID:        status.PID,
			HookStatus: status.HookStatus,
			UpdatedAt:  status.UpdatedAt,
		}
		if status.LastTransition != nil {
			row.TransitionEvent = status.LastTransition.Event
			row.TransitionSource = status.LastTransition.Source
		}

		if row.State == agent.StateBlocked {
			blocked = append(blocked, row)
		} else {
			other = append(other, row)
		}
	}
	return append(blocked, other...)
}

func (f fleetFilter) matches(state agent.State) bool {
	switch f {
	case filterAll:
		return true
	case filterPending:
		return state == agent.StatePending
	case filterStarting:
		return state == agent.StateStarting
	case filterWorking:
		return state == agent.StateWorking
	case filterBlocked:
		return state == agent.StateBlocked
	case filterIdle:
		return state == agent.StateIdle
	case filterDone:
		return state == agent.StateDone
	case filterStopped:
		return state == agent.StateStopped
	default:
		return false
	}
}

func reconcileSelection(current selection, rows []fleetRow) selection {
	if len(rows) == 0 {
		return selection{}
	}
	for index := range rows {
		if rows[index].AgentID == current.AgentID {
			return selection{
				AgentID:       current.AgentID,
				FallbackIndex: index,
			}
		}
	}

	index := clampIndex(current.FallbackIndex, len(rows))
	return selection{
		AgentID:       rows[index].AgentID,
		FallbackIndex: index,
	}
}

func moveSelection(current selection, rows []fleetRow, delta int) selection {
	current = reconcileSelection(current, rows)
	if len(rows) == 0 {
		return current
	}

	index := clampIndex(current.FallbackIndex+delta, len(rows))
	return selection{
		AgentID:       rows[index].AgentID,
		FallbackIndex: index,
	}
}

func clampIndex(index, length int) int {
	switch {
	case index < 0:
		return 0
	case index >= length:
		return length - 1
	default:
		return index
	}
}
