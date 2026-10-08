package adapter

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Duang777/drove/internal/agent"
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
