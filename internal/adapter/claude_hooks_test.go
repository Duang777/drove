package adapter

import (
	"testing"

	"github.com/Duang777/drove/internal/detect"
)

func TestClaudeNotificationMapping(t *testing.T) {
	tests := []struct {
		notification string
		wantKind     detect.Kind
		wantStored   string
	}{
		{notification: "permission_prompt", wantKind: detect.KindHumanInputRequired, wantStored: "permission_prompt"},
		{notification: "elicitation_dialog", wantKind: detect.KindHumanInputRequired, wantStored: "elicitation_dialog"},
		{notification: "elicitation_url_dialog", wantKind: detect.KindHumanInputRequired, wantStored: "elicitation_url_dialog"},
		{notification: "agent_needs_input", wantKind: detect.KindHumanInputRequired, wantStored: "agent_needs_input"},
		{notification: "quota_auto_resume_stale", wantKind: detect.KindHumanInputRequired, wantStored: "quota_auto_resume_stale"},
		{notification: "idle_prompt", wantKind: detect.KindIdlePrompt, wantStored: "idle_prompt"},
		{notification: "auth_success", wantKind: detect.KindHumanInputResolved, wantStored: "auth_success"},
		{notification: "elicitation_complete", wantKind: detect.KindHumanInputResolved, wantStored: "elicitation_complete"},
		{notification: "elicitation_response", wantKind: detect.KindHumanInputResolved, wantStored: "elicitation_response"},
		{notification: "agent_completed", wantKind: detect.KindTaskCompleted, wantStored: "agent_completed"},
		{notification: "quota_auto_resume_fired", wantKind: detect.KindObserved, wantStored: "quota_auto_resume_fired"},
		{notification: "quota_auto_resume_disabled", wantKind: detect.KindObserved, wantStored: "quota_auto_resume_disabled"},
		{notification: "future_notification", wantKind: detect.KindObserved, wantStored: "other"},
	}

	for _, test := range tests {
		t.Run(test.notification, func(t *testing.T) {
			kind, _, stored := classifyClaudeNotification(test.notification)
			if kind != test.wantKind || stored != test.wantStored {
				t.Fatalf(
					"notification %q = kind %s stored %q, want %s %q",
					test.notification,
					kind,
					stored,
					test.wantKind,
					test.wantStored,
				)
			}
		})
	}
}
