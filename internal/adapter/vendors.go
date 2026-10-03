package adapter

import (
	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
)

// claudeRunner 适配 Anthropic Claude Code CLI。
type claudeRunner struct{}

func (claudeRunner) Vendor() string { return "claude" }
func (claudeRunner) Command(mode agent.RunMode) (string, []string) {
	if mode == agent.RunModeOneshot {
		return "claude", []string{"--print"}
	}
	return "claude", nil
}

// claudeHeuristic 从 claude 输出中识别状态信号。
type claudeHeuristic struct{}

func (claudeHeuristic) Classify(line string) (OutputHint, bool) {
	switch {
	case containsAny(line, "Waiting for your input", "Waiting for response", "Choose an option"):
		return OutputHint{
			Kind:       detect.KindHeuristicBlocked,
			Confidence: 0.9,
			Evidence:   "claude awaiting input",
		}, true
	case containsAny(line, "Error", "error:", "✖", "Failed"):
		return OutputHint{
			Kind:       detect.KindHeuristicBlocked,
			Confidence: 0.6,
			Evidence:   "claude reported error",
		}, true
	case containsAny(line, "Task complete", "Done!", "Completed"):
		return OutputHint{
			Kind:       detect.KindTaskCompleted,
			Confidence: 0.8,
			Evidence:   "claude reported task completion",
		}, true
	default:
		return OutputHint{}, false
	}
}

// codexRunner 适配 OpenAI Codex CLI。
type codexRunner struct{}

func (codexRunner) Vendor() string { return "codex" }
func (codexRunner) Command(mode agent.RunMode) (string, []string) {
	if mode == agent.RunModeOneshot {
		return "codex", []string{"exec"}
	}
	return "codex", nil
}

// codexHeuristic 从 codex 输出中识别状态信号。
type codexHeuristic struct{}

func (codexHeuristic) Classify(line string) (OutputHint, bool) {
	switch {
	case containsAny(line, "Waiting for user", "Please choose", "Select an option"):
		return OutputHint{
			Kind:       detect.KindHeuristicBlocked,
			Confidence: 0.9,
			Evidence:   "codex awaiting input",
		}, true
	case containsAny(line, "❌", "Error", "error:"):
		return OutputHint{
			Kind:       detect.KindHeuristicBlocked,
			Confidence: 0.6,
			Evidence:   "codex reported error",
		}, true
	default:
		return OutputHint{}, false
	}
}

// genericRunner 兜底：直接运行用户命令，无启发式。
type genericRunner struct{}

func (genericRunner) Vendor() string { return "generic" }
func (genericRunner) Command(agent.RunMode) (string, []string) {
	return "", nil
}

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
