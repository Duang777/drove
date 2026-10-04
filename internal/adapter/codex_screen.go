package adapter

import (
	"time"

	"github.com/Duang777/drove/internal/detect"
)

func codexScreenRules() []screenRuleDefinition {
	bottom := screenRegion{name: screenRegionBottom, bottomRows: 12}
	approvalCleared := screenRuleEdge{
		kind:         detect.KindHumanInputResolved,
		confirmation: 500 * time.Millisecond,
	}
	return []screenRuleDefinition{
		{
			name:       "codex.approval_prompt",
			region:     bottom,
			evidence:   "approval prompt",
			confidence: 1,
			pattern: `(?ims)^\s*(?:Would you like to|Do you want to approve).*$` +
				`.*^\s*Press enter to confirm or esc to cancel\s*$`,
			present: screenRuleEdge{
				kind:         detect.KindHumanInputRequired,
				confirmation: 750 * time.Millisecond,
			},
			cleared: &approvalCleared,
		},
		{
			name:       "codex.idle_prompt",
			region:     bottom,
			evidence:   "idle prompt",
			confidence: 1,
			pattern:    `(?m)^\s*›\s+Ask Codex to do anything\s*$`,
			present: screenRuleEdge{
				kind:         detect.KindIdlePrompt,
				confirmation: time.Second,
			},
		},
	}
}
