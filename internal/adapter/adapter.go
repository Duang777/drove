// Package adapter 提供跨厂商 agent 的统一接入抽象。
//
// 本包是唯一允许出现厂商专属逻辑（命令、参数、状态启发式）的地方。
package adapter

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/term"
)

// OutputHint is the bounded classification of one sanitized terminal line.
type OutputHint struct {
	// Kind is heuristic evidence, not a direct Agent state.
	Kind detect.Kind
	// Confidence 置信度 0~1（供上层结合其它信号决策）。
	Confidence float64
	// Evidence is a redacted explanation suitable for audit.
	Evidence string
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
	Classify(line string) (OutputHint, bool)
}

// HookInput is the transport-neutral input to a vendor hook normalizer.
type HookInput struct {
	DeliveryID string
	ReceivedAt time.Time
	Payload    json.RawMessage
}

// HookNormalizer converts allowlisted vendor JSON into a redacted signal.
type HookNormalizer interface {
	NormalizeHook(HookInput) (detect.Signal, error)
}

// Entry 是注册表中的一个实现。
type Entry struct {
	Runner         Runner
	Heuristic      Heuristic
	HookNormalizer HookNormalizer
	SignalInjector SignalInjector
}

// Classify sanitizes terminal control sequences before invoking the vendor heuristic.
func (e Entry) Classify(line string) (OutputHint, bool) {
	if e.Heuristic == nil {
		return OutputHint{}, false
	}
	return e.Heuristic.Classify(term.StripString(line))
}

// SupportsHooks reports whether this vendor can decode command-hook payloads.
func (e Entry) SupportsHooks() bool {
	return e.HookNormalizer != nil
}

// NormalizeHook normalizes one vendor hook payload without retaining the raw JSON.
func (e Entry) NormalizeHook(input HookInput) (detect.Signal, error) {
	if e.HookNormalizer == nil {
		return detect.Signal{}, ErrUnsupportedHook
	}
	return e.HookNormalizer.NormalizeHook(input)
}

// SupportsSignalInjection reports whether the vendor has a session-only plan.
func (e Entry) SupportsSignalInjection() bool {
	return e.SignalInjector != nil
}

// InjectSignals builds a copied session-only launch plan.
func (e Entry) InjectSignals(
	request SignalInjectionRequest,
) (SignalInjectionPlan, error) {
	if e.SignalInjector == nil {
		return SignalInjectionPlan{}, ErrUnsupportedSignalInjection
	}
	return injectSignals(e.SignalInjector, request)
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
	r.register(
		claudeRunner{},
		claudeHeuristic{},
		claudeHookDecoder{},
		claudeSignalInjector{},
	)
	r.register(
		codexRunner{},
		codexHeuristic{},
		codexHookDecoder{},
		codexSignalInjector{},
	)
	r.generic = Entry{Runner: genericRunner{}, Heuristic: nil}
	return r
}

// register 注册一个实现（panic 防重复注册，属开发期错误）。
func (r *Registry) register(
	runner Runner,
	heuristic Heuristic,
	normalizer HookNormalizer,
	injector SignalInjector,
) {
	v := runner.Vendor()
	if _, dup := r.entries[v]; dup {
		panic(fmt.Sprintf("adapter: duplicate vendor %q", v))
	}
	r.entries[v] = Entry{
		Runner:         runner,
		Heuristic:      heuristic,
		HookNormalizer: normalizer,
		SignalInjector: injector,
	}
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

// Lookup returns only an exact registered vendor.
func (r *Registry) Lookup(vendor string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[vendor]
	if ok {
		return entry, true
	}
	if vendor == "generic" {
		return r.generic, true
	}
	return Entry{}, false
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
