package agent

import "testing"

func TestValidActionKind(t *testing.T) {
	for _, kind := range []ActionKind{
		ActionApprove,
		ActionDeny,
		ActionReply,
	} {
		if !ValidActionKind(kind) {
			t.Fatalf("ValidActionKind(%q) = false", kind)
		}
	}
	for _, kind := range []ActionKind{"", "auto_approve", "reply\n"} {
		if ValidActionKind(kind) {
			t.Fatalf("ValidActionKind(%q) = true", kind)
		}
	}
}
