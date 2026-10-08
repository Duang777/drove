package adapter

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/term"
)

func TestApprovalPlansUseVendorScreenRules(t *testing.T) {
	tests := []struct {
		vendor string
		want   map[agent.ActionKind][]byte
	}{
		{
			vendor: "claude",
			want: map[agent.ActionKind][]byte{
				agent.ActionApprove: []byte("1\r"),
				agent.ActionDeny:    {0x1b},
				agent.ActionReply:   []byte("2\ruse read-only\r"),
			},
		},
		{
			vendor: "codex",
			want: map[agent.ActionKind][]byte{
				agent.ActionApprove: []byte("\r"),
				agent.ActionDeny:    {0x1b},
				agent.ActionReply:   []byte("\x1buse read-only\r"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.vendor, func(t *testing.T) {
			classifier, err := NewRegistry().For(test.vendor).NewScreenClassifier()
			if err != nil {
				t.Fatalf("NewScreenClassifier: %v", err)
			}
			controller := newScreenTestController(t)
			defer closeScreenTestController(t, controller)
			writeScreenFixture(
				t,
				controller,
				loadScreenFixture(t, test.vendor, "approval"),
				screenFeedMode{name: "complete", sizes: []int{4096}},
			)
			snapshot, err := controller.Snapshot()
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}

			actions := classifier.AvailableApprovalActions(snapshot)
			wantActions := []agent.ActionKind{
				agent.ActionApprove,
				agent.ActionDeny,
				agent.ActionReply,
			}
			if len(actions) != len(wantActions) {
				t.Fatalf("actions = %v, want %v", actions, wantActions)
			}
			for index := range wantActions {
				if actions[index] != wantActions[index] {
					t.Fatalf("actions = %v, want %v", actions, wantActions)
				}
			}

			for kind, want := range test.want {
				reply := ""
				if kind == agent.ActionReply {
					reply = "use read-only"
				}
				plan, err := classifier.PlanApprovalAction(snapshot, kind, reply)
				if err != nil {
					t.Fatalf("PlanApprovalAction(%q): %v", kind, err)
				}
				if plan.Kind() != kind ||
					plan.Rule() != test.vendor+".approval_prompt" ||
					!bytes.Equal(plan.Bytes(), want) {
					t.Fatalf("plan = kind %q rule %q bytes %q, want %q", plan.Kind(), plan.Rule(), plan.Bytes(), want)
				}
				got := plan.Bytes()
				got[0] ^= 0xff
				if bytes.Equal(got, plan.Bytes()) {
					t.Fatal("Bytes returned shared mutable storage")
				}
			}
		})
	}
}

func TestApprovalPlansRequireCurrentApprovalPrompt(t *testing.T) {
	for _, vendor := range []string{"claude", "codex"} {
		t.Run(vendor, func(t *testing.T) {
			classifier, err := NewRegistry().For(vendor).NewScreenClassifier()
			if err != nil {
				t.Fatalf("NewScreenClassifier: %v", err)
			}
			controller := newScreenTestController(t)
			defer closeScreenTestController(t, controller)
			writeScreenFixture(
				t,
				controller,
				loadScreenFixture(t, vendor, "idle_after_approval"),
				screenFeedMode{name: "complete", sizes: []int{4096}},
			)
			snapshot, err := controller.Snapshot()
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if actions := classifier.AvailableApprovalActions(snapshot); len(actions) != 0 {
				t.Fatalf("idle actions = %v, want none", actions)
			}
			if _, err := classifier.PlanApprovalAction(
				snapshot,
				agent.ActionApprove,
				"",
			); !errors.Is(err, ErrApprovalPromptUnavailable) {
				t.Fatalf("approval error = %v, want unavailable", err)
			}
		})
	}
}

func TestGenericClassifierHasNoApprovalPlan(t *testing.T) {
	classifier, err := NewRegistry().For("generic").NewScreenClassifier()
	if err != nil {
		t.Fatalf("NewScreenClassifier: %v", err)
	}
	snapshot := screenTestSnapshot(t, "Do you want to proceed?\nEsc to cancel")
	if actions := classifier.AvailableApprovalActions(snapshot); len(actions) != 0 {
		t.Fatalf("generic actions = %v, want none", actions)
	}
	if _, err := classifier.PlanApprovalAction(
		snapshot,
		agent.ActionKind("later"),
		"",
	); !errors.Is(err, ErrApprovalPromptUnavailable) {
		t.Fatalf("generic approval error = %v, want unavailable", err)
	}
}

func TestApprovalPlanValidatesReplyText(t *testing.T) {
	classifier, err := NewRegistry().For("claude").NewScreenClassifier()
	if err != nil {
		t.Fatalf("NewScreenClassifier: %v", err)
	}
	snapshot := approvalTestSnapshot(t, "claude")

	tests := []struct {
		name  string
		kind  agent.ActionKind
		reply string
	}{
		{name: "approve with reply", kind: agent.ActionApprove, reply: "unexpected"},
		{name: "deny with reply", kind: agent.ActionDeny, reply: "unexpected"},
		{name: "empty reply", kind: agent.ActionReply},
		{name: "whitespace reply", kind: agent.ActionReply, reply: " \t "},
		{name: "invalid UTF-8", kind: agent.ActionReply, reply: string([]byte{0xff})},
		{name: "C0 control", kind: agent.ActionReply, reply: "first\nsecond"},
		{name: "C1 control", kind: agent.ActionReply, reply: "first\u0085second"},
		{name: "too large", kind: agent.ActionReply, reply: strings.Repeat("x", 4097)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := classifier.PlanApprovalAction(snapshot, test.kind, test.reply)
			if !errors.Is(err, ErrApprovalReplyInvalid) {
				t.Fatalf("PlanApprovalAction error = %v, want ErrApprovalReplyInvalid", err)
			}
			if test.reply != "" && strings.Contains(err.Error(), test.reply) {
				t.Fatal("validation error contains reply text")
			}
		})
	}

	plan, err := classifier.PlanApprovalAction(
		snapshot,
		agent.ActionReply,
		"  use read-only  ",
	)
	if err != nil {
		t.Fatalf("PlanApprovalAction trimmed reply: %v", err)
	}
	if got, want := plan.Bytes(), []byte("2\ruse read-only\r"); !bytes.Equal(got, want) {
		t.Fatalf("plan bytes = %q, want %q", got, want)
	}
	if plan.ReplyBytes() != len("use read-only") {
		t.Fatalf("reply bytes = %d, want %d", plan.ReplyBytes(), len("use read-only"))
	}
}

func approvalTestSnapshot(t *testing.T, vendor string) term.Snapshot {
	t.Helper()
	controller := newScreenTestController(t)
	t.Cleanup(func() {
		closeScreenTestController(t, controller)
	})
	writeScreenFixture(
		t,
		controller,
		loadScreenFixture(t, vendor, "approval"),
		screenFeedMode{name: "complete", sizes: []int{4096}},
	)
	snapshot, err := controller.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return snapshot
}
