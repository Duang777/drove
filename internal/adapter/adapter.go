// Package adapter 提供跨厂商 agent 的统一接入抽象。
//
// 本包是唯一允许出现厂商专属逻辑（命令、参数、屏幕规则）的地方。
package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/detect"
	"github.com/Duang777/drove/internal/term"
)

// Runner 描述如何拉起某厂商的 agent。
type Runner interface {
	// Vendor 返回厂商标识（如 "claude"）。
	Vendor() string
	// Command 返回指定运行模式的可执行文件与参数（不含环境变量透传）。
	Command(mode agent.RunMode) (name string, args []string)
}

// Command is one executable and its argument vector.
type Command struct {
	Name string
	Args []string
}

// CreationMeta is the persisted creation data needed to resume a session.
type CreationMeta struct {
	Mode agent.RunMode
}

// Resumer describes a vendor's native session-resume command.
type Resumer interface {
	ResumeCommand(meta CreationMeta, ref string) (Command, error)
}

var (
	// ErrUnsupportedResume indicates that the vendor has no native resume command.
	ErrUnsupportedResume = errors.New("adapter: vendor resume is unsupported")
)

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

// TerminalNotificationNormalizer converts a framed terminal notice to a signal.
type TerminalNotificationNormalizer interface {
	NormalizeOSC9(term.OSC9Frame) (detect.Signal, bool, error)
}

type terminalNotificationSanitizerFactory interface {
	NewOSC9Sanitizer() (*term.OSC9Sanitizer, error)
}

// Entry 是注册表中的一个实现。
type Entry struct {
	Runner                         Runner
	HookNormalizer                 HookNormalizer
	SignalInjector                 SignalInjector
	TerminalNotificationNormalizer TerminalNotificationNormalizer
	screenRules                    screenRuleProvider
}

// NewScreenClassifier constructs independent screen-rule state for one session.
func (e Entry) NewScreenClassifier() (*ScreenClassifier, error) {
	if e.Runner == nil {
		return nil, fmt.Errorf("adapter: screen classifier runner is required")
	}
	var definitions []screenRuleDefinition
	if e.screenRules != nil {
		definitions = e.screenRules()
	}
	return newScreenClassifier(e.Runner.Vendor(), definitions)
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

// SupportsTerminalNotifications reports whether the adapter understands OSC 9.
func (e Entry) SupportsTerminalNotifications() bool {
	return e.TerminalNotificationNormalizer != nil
}

// NormalizeOSC9 interprets one framed OSC 9 notification.
func (e Entry) NormalizeOSC9(
	frame term.OSC9Frame,
) (detect.Signal, bool, error) {
	if e.TerminalNotificationNormalizer == nil {
		return detect.Signal{}, false, nil
	}
	return e.TerminalNotificationNormalizer.NormalizeOSC9(frame)
}

// NewOSC9Sanitizer constructs output sanitization state for this adapter.
func (e Entry) NewOSC9Sanitizer() (*term.OSC9Sanitizer, error) {
	factory, ok := e.TerminalNotificationNormalizer.(terminalNotificationSanitizerFactory)
	if !ok {
		return nil, errors.New(
			"adapter: terminal notification sanitizer is unsupported",
		)
	}
	sanitizer, err := factory.NewOSC9Sanitizer()
	if err != nil {
		return nil, fmt.Errorf(
			"adapter: create terminal notification sanitizer: %w",
			err,
		)
	}
	return sanitizer, nil
}

// SupportsResume reports whether the exact runner supports native resume.
func (e Entry) SupportsResume() bool {
	_, ok := e.Runner.(Resumer)
	return ok
}

// ResumeCommand returns the vendor's native resume command.
func (e Entry) ResumeCommand(meta CreationMeta, ref string) (Command, error) {
	resumer, ok := e.Runner.(Resumer)
	if !ok {
		return Command{}, ErrUnsupportedResume
	}
	if !agent.ValidRunMode(meta.Mode) {
		return Command{}, fmt.Errorf("adapter: invalid creation mode %q", meta.Mode)
	}
	if !validVendorSessionRef(ref) {
		return Command{}, errors.New(
			"adapter: vendor session reference must contain 1 to 256 printable ASCII bytes",
		)
	}
	command, err := resumer.ResumeCommand(meta, ref)
	if err != nil {
		return Command{}, err
	}
	if command.Name == "" {
		return Command{}, errors.New("adapter: resume command name is empty")
	}
	command.Args = append([]string(nil), command.Args...)
	return command, nil
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
		claudeHookDecoder{},
		claudeSignalInjector{},
		nil,
		claudeScreenRules,
	)
	r.register(
		codexRunner{},
		codexHookDecoder{},
		codexSignalInjector{},
		codexOSC9Normalizer{},
		codexScreenRules,
	)
	r.generic = Entry{Runner: genericRunner{}}
	return r
}

// register 注册一个实现（panic 防重复注册，属开发期错误）。
func (r *Registry) register(
	runner Runner,
	normalizer HookNormalizer,
	injector SignalInjector,
	terminalNormalizer TerminalNotificationNormalizer,
	screenRules screenRuleProvider,
) {
	v := runner.Vendor()
	if _, dup := r.entries[v]; dup {
		panic(fmt.Sprintf("adapter: duplicate vendor %q", v))
	}
	r.entries[v] = Entry{
		Runner:                         runner,
		HookNormalizer:                 normalizer,
		SignalInjector:                 injector,
		TerminalNotificationNormalizer: terminalNormalizer,
		screenRules:                    screenRules,
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

func validVendorSessionRef(ref string) bool {
	if len(ref) == 0 || len(ref) > 256 {
		return false
	}
	for i := range len(ref) {
		if ref[i] < 0x20 || ref[i] > 0x7e {
			return false
		}
	}
	return ref[0] != ' ' && ref[len(ref)-1] != ' '
}
