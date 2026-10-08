package agent

// ActionKind identifies one vendor-neutral response to an approval prompt.
type ActionKind string

const (
	// ActionApprove accepts the pending approval request.
	ActionApprove ActionKind = "approve"
	// ActionDeny rejects the pending approval request without a message.
	ActionDeny ActionKind = "deny"
	// ActionReply rejects the pending approval request with a user message.
	ActionReply ActionKind = "reply"
)

// ValidActionKind reports whether kind is a supported explicit user action.
func ValidActionKind(kind ActionKind) bool {
	switch kind {
	case ActionApprove, ActionDeny, ActionReply:
		return true
	default:
		return false
	}
}
