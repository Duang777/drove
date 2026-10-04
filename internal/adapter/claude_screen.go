package adapter

import (
	"time"

	"github.com/Duang777/drove/internal/detect"
)

func claudeScreenRules() []screenRuleDefinition {
	bottom := screenRegion{name: screenRegionBottom, bottomRows: 12}
	approvalCleared := screenRuleEdge{
		kind:         detect.KindHumanInputResolved,
		confirmation: 500 * time.Millisecond,
	}
	return []screenRuleDefinition{
		{
			name:       "claude.approval_prompt",
			region:     bottom,
			evidence:   "approval prompt",
			confidence: 1,
			pattern: `(?ims)^\s*Do you want to proceed\?\s*$` +
				`.*^\s*Esc to cancel\s*$`,
			present: screenRuleEdge{
				kind:         detect.KindHumanInputRequired,
				confirmation: 750 * time.Millisecond,
			},
			cleared: &approvalCleared,
		},
		{
			name:       "claude.idle_prompt",
			region:     bottom,
			evidence:   "idle prompt",
			confidence: 1,
			pattern: `(?ms)^\s*❯(?:\s+Try .*)?\s*$` +
				`.*^\s*\? for shortcuts\s*$`,
			present: screenRuleEdge{
				kind:         detect.KindIdlePrompt,
				confirmation: time.Second,
			},
		},
		{
			name:       "claude.interrupted",
			region:     bottom,
			evidence:   "interrupt result",
			confidence: 1,
			pattern: `(?m)^\s*Interrupted\s*$` +
				`\n^\s*What should Claude do instead\?\s*$`,
			present: screenRuleEdge{
				kind:         detect.KindInterrupted,
				confirmation: time.Second,
			},
		},
	}
}
