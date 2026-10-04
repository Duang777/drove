package adapter

import (
	"github.com/Duang777/drove/internal/agent"
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

func (claudeRunner) ResumeCommand(_ CreationMeta, ref string) (Command, error) {
	return Command{Name: "claude", Args: []string{"--resume", ref}}, nil
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

func (codexRunner) ResumeCommand(_ CreationMeta, ref string) (Command, error) {
	return Command{Name: "codex", Args: []string{"resume", ref}}, nil
}

// genericRunner 兜底：直接运行用户命令。
type genericRunner struct{}

func (genericRunner) Vendor() string { return "generic" }
func (genericRunner) Command(agent.RunMode) (string, []string) {
	return "", nil
}
