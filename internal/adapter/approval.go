package adapter

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/term"
)

var (
	// ErrApprovalPromptUnavailable means the current screen has no supported prompt.
	ErrApprovalPromptUnavailable = errors.New("adapter: approval prompt is unavailable")
	// ErrApprovalActionUnsupported means the matched prompt cannot perform the action.
	ErrApprovalActionUnsupported = errors.New("adapter: approval action is unsupported")
	// ErrApprovalReplyInvalid means the reply violates the bounded text contract.
	ErrApprovalReplyInvalid = errors.New("adapter: approval reply is invalid")
)

type approvalActionBinding struct {
	prefix       []byte
	suffix       []byte
	includeReply bool
}

type approvalDefinition struct {
	bindings map[agent.ActionKind]approvalActionBinding
}

// ApprovalActionPlan is an immutable vendor-specific input plan.
type ApprovalActionPlan struct {
	kind       agent.ActionKind
	rule       string
	input      []byte
	replyBytes int
}

// Kind returns the normalized action represented by this plan.
func (p ApprovalActionPlan) Kind() agent.ActionKind {
	return p.kind
}

// Rule returns the stable adapter rule that produced this plan.
func (p ApprovalActionPlan) Rule() string {
	return p.rule
}

// Bytes returns a copy of the PTY input bytes.
func (p ApprovalActionPlan) Bytes() []byte {
	return append([]byte(nil), p.input...)
}

// ReplyBytes returns the normalized reply size recorded in the action audit.
func (p ApprovalActionPlan) ReplyBytes() int {
	return p.replyBytes
}

// AvailableApprovalActions reports actions for the approval prompt visible now.
func (c *ScreenClassifier) AvailableApprovalActions(
	snapshot term.Snapshot,
) []agent.ActionKind {
	rule := c.matchingApprovalRule(snapshot)
	if rule == nil {
		return nil
	}
	actions := make([]agent.ActionKind, 0, len(rule.definition.approval.bindings))
	for _, kind := range []agent.ActionKind{
		agent.ActionApprove,
		agent.ActionDeny,
		agent.ActionReply,
	} {
		if _, ok := rule.definition.approval.bindings[kind]; ok {
			actions = append(actions, kind)
		}
	}
	return actions
}

// PlanApprovalAction maps one normalized action against the approval prompt
// visible in snapshot. It does not mutate classifier edge state.
func (c *ScreenClassifier) PlanApprovalAction(
	snapshot term.Snapshot,
	kind agent.ActionKind,
	reply string,
) (ApprovalActionPlan, error) {
	normalizedReply, replyBytes, err := validateApprovalReply(kind, reply)
	if err != nil {
		return ApprovalActionPlan{}, err
	}
	rule := c.matchingApprovalRule(snapshot)
	if rule == nil {
		return ApprovalActionPlan{}, ErrApprovalPromptUnavailable
	}
	binding, ok := rule.definition.approval.bindings[kind]
	if !ok {
		return ApprovalActionPlan{}, fmt.Errorf(
			"%w: %q",
			ErrApprovalActionUnsupported,
			kind,
		)
	}
	size := len(binding.prefix) + len(binding.suffix)
	if binding.includeReply {
		size += len(normalizedReply)
	}
	input := make([]byte, 0, size)
	input = append(input, binding.prefix...)
	if binding.includeReply {
		input = append(input, normalizedReply...)
	}
	input = append(input, binding.suffix...)
	return ApprovalActionPlan{
		kind:       kind,
		rule:       rule.definition.name,
		input:      input,
		replyBytes: replyBytes,
	}, nil
}

func validateApprovalReply(
	kind agent.ActionKind,
	reply string,
) (string, int, error) {
	if kind != agent.ActionReply {
		if reply != "" {
			return "", 0, ErrApprovalReplyInvalid
		}
		return "", 0, nil
	}
	if !utf8.ValidString(reply) {
		return "", 0, ErrApprovalReplyInvalid
	}
	normalized := strings.TrimSpace(reply)
	if len(normalized) == 0 || len(normalized) > 4096 {
		return "", 0, ErrApprovalReplyInvalid
	}
	for _, value := range normalized {
		if value <= 0x1f || value >= 0x7f && value <= 0x9f {
			return "", 0, ErrApprovalReplyInvalid
		}
	}
	return normalized, len(normalized), nil
}

func (c *ScreenClassifier) matchingApprovalRule(
	snapshot term.Snapshot,
) *compiledScreenRule {
	if c == nil {
		return nil
	}
	regions := make(map[screenRegion]string)
	for index := range c.rules {
		rule := &c.rules[index]
		if rule.definition.approval == nil {
			continue
		}
		text, ok := regions[rule.definition.region]
		if !ok {
			text = screenRegionText(snapshot, rule.definition.region)
			regions[rule.definition.region] = text
		}
		if rule.matcher.MatchString(text) {
			return rule
		}
	}
	return nil
}
