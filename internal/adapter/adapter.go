// Package adapter 提供跨厂商 agent 的统一接入抽象。
//
// 本包是唯一允许出现厂商专属逻辑（命令、参数、状态启发式）的地方。
package adapter

import (
	"fmt"
	"sync"

	"github.com/Duang777/drove/internal/agent"
)

// StateHint 是适配器对某行输出给出的状态提示。
type StateHint struct {
	// State 建议的目标状态；agent.StateStarting 表示"无结论"。
	State agent.State
	// Confidence 置信度 0~1（供上层结合其它信号决策）。
	Confidence float64
	// Reason 供审计。
	Reason string
}

// Runner 描述如何拉起某厂商的 agent。
type Runner interface {
	// Vendor 返回厂商标识（如 "claude"）。
	Vendor() string
	// Command 返回指定运行模式的可执行文件与参数（不含环境变量透传）。
	Command(mode agent.RunMode) (name string, args []string)
}

// Heuristic 从输出行推断状态信号。
type Heuristic interface {
	// Classify 返回状态提示；ok=false 表示该行无可信信号。
	Classify(line string) (StateHint, bool)
}

// Entry 是注册表中的一个实现。
type Entry struct {
	Runner    Runner
	Heuristic Heuristic
}

// Registry 按厂商标识注册与查找适配器。
type Registry struct {
	mu      sync.RWMutex
	entries map[string]Entry
	generic Entry
}

// NewRegistry 创建注册表并内置 claude / codex / generic 三款适配器。
func NewRegistry() *Registry {
	r := &Registry{entries: make(map[string]Entry)}
	r.register(claudeRunner{}, claudeHeuristic{})
	r.register(codexRunner{}, codexHeuristic{})
	r.generic = Entry{Runner: genericRunner{}, Heuristic: nil}
	return r
}

// register 注册一个实现（panic 防重复注册，属开发期错误）。
func (r *Registry) register(runner Runner, heur Heuristic) {
	v := runner.Vendor()
	if _, dup := r.entries[v]; dup {
		panic(fmt.Sprintf("adapter: duplicate vendor %q", v))
	}
	r.entries[v] = Entry{Runner: runner, Heuristic: heur}
}

// For 返回指定厂商的实现；未知厂商回退 generic（无启发式）。
func (r *Registry) For(vendor string) Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.entries[vendor]; ok {
		return e
	}
	return r.generic
}

// Vendors 返回全部已注册厂商标识（含 generic 兜底）。
func (r *Registry) Vendors() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.entries)+1)
	for v := range r.entries {
		out = append(out, v)
	}
	out = append(out, "generic")
	return out
}
