package adapter

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/term"
)

var codexApprovalPrefixes = [...]string{
	"Approval requested: ",
	"Codex wants to edit ",
	"Approval requested by ",
}

type codexOSC9Normalizer struct{}

func (codexOSC9Normalizer) NewOSC9Sanitizer() (*term.OSC9Sanitizer, error) {
	return term.NewOSC9Sanitizer(codexApprovalPrefixes[:]...)
}

func (codexOSC9Normalizer) NormalizeOSC9(
	frame term.OSC9Frame,
) (detect.Signal, bool, error) {
	payload := frame.Payload()
	if !utf8.Valid(payload) {
		return detect.Signal{}, false, nil
	}
	text := string(payload)
	matched := false
	for _, prefix := range codexApprovalPrefixes {
		if strings.HasPrefix(text, prefix) {
			matched = true
			break
		}
	}
	if !matched {
		return detect.Signal{}, false, nil
	}

	attribution, err := agent.NewTerminalAttribution(
		"osc9",
		frame.OutputOffset(),
		frame.LastOutputSeq(),
	)
	if err != nil {
		return detect.Signal{}, false, fmt.Errorf(
			"adapter: create Codex terminal attribution: %w",
			err,
		)
	}
	signal, err := detect.NewTerminalNotifySignal(detect.Signal{
		Kind:         detect.KindPermissionRequested,
		Vendor:       "codex",
		VendorEvent:  "tui_notification",
		Scope:        detect.ScopeRoot,
		Notification: "approval-requested",
		Evidence:     "approval requested",
		Confidence:   1,
		ReceivedAt:   frame.CommittedAt(),
		Terminal:     &attribution,
	})
	if err != nil {
		return detect.Signal{}, false, fmt.Errorf(
			"adapter: normalize Codex terminal notification: %w",
			err,
		)
	}
	return signal, true, nil
}
