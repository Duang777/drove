package adapter

import "github.com/Duang777/drove/internal/agent"

// claudeRunner 适配 Anthropic Claude Code CLI。
type claudeRunner struct{}

func (claudeRunner) Vendor() string { return "claude" }
func (claudeRunner) Command() (string, []string) {
	return "claude", []string{"--print"}
}

// claudeHeuristic 从 claude 输出中识别状态信号。
type claudeHeuristic struct{}

func (claudeHeuristic) Classify(line string) (StateHint, bool) {
	switch {
	case containsAny(line, "Waiting for your input", "Waiting for response", "Choose an option"):
		return StateHint{State: agent.StateBlocked, Confidence: 0.9, Reason: "claude awaiting input"}, true
	case containsAny(line, "Error", "error:", "✖", "Failed"):
		return StateHint{State: agent.StateBlocked, Confidence: 0.6, Reason: "claude reported error"}, true
	case containsAny(line, "Task complete", "Done!", "Completed"):
		return StateHint{State: agent.StateDone, Confidence: 0.8, Reason: "claude finished task"}, true
	default:
		return StateHint{}, false
	}
}

// codexRunner 适配 OpenAI Codex CLI。
type codexRunner struct{}

func (codexRunner) Vendor() string { return "codex" }
func (codexRunner) Command() (string, []string) {
	return "codex", []string{"exec"}
}

// codexHeuristic 从 codex 输出中识别状态信号。
type codexHeuristic struct{}

func (codexHeuristic) Classify(line string) (StateHint, bool) {
	switch {
	case containsAny(line, "Waiting for user", "Please choose", "Select an option"):
		return StateHint{State: agent.StateBlocked, Confidence: 0.9, Reason: "codex awaiting input"}, true
	case containsAny(line, "❌", "Error", "error:"):
		return StateHint{State: agent.StateBlocked, Confidence: 0.6, Reason: "codex reported error"}, true
	default:
		return StateHint{}, false
	}
}

// genericRunner 兜底：直接运行用户命令，无启发式。
type genericRunner struct{}

func (genericRunner) Vendor() string              { return "generic" }
func (genericRunner) Command() (string, []string) { return "", nil }

// containsAny 报告 s 是否包含 patterns 中的任意子串。
func containsAny(s string, patterns ...string) bool {
	for _, p := range patterns {
		if p != "" && contains(s, p) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(sub) > 0 && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
