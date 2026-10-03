// Package agent 定义 Agent 抽象与状态机。
//
// 本包是全项目唯一的"状态权威"：Agent 的状态只能通过
// Transition 变更，任何组件不得直接修改 Agent 的内部状态字段。
package agent

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ID 是 agent 的稳定标识（UUID 字符串）。
type ID string

// State 表示一个 agent 的运行时状态。
type State string

// RunMode 控制 agent 进程的启动方式和自然退出语义。
type RunMode string

const (
	// RunModeInteractive 启动常驻交互进程。
	RunModeInteractive RunMode = "interactive"
	// RunModeOneshot 启动执行一次后退出的进程。
	RunModeOneshot RunMode = "oneshot"

	// StatePending 已创建、尚未启动（初始态）。
	StatePending State = "pending"
	// StateStarting 进程已拉起、尚未就绪（生命周期边界态）。
	StateStarting State = "starting"
	// StateWorking 正在执行任务。
	StateWorking State = "working"
	// StateBlocked 等待输入/工具结果/人工介入，无法自行推进。
	StateBlocked State = "blocked"
	// StateDone 任务完成或已退出且无错误。
	StateDone State = "done"
	// StateIdle 存活但当前无任务。
	StateIdle State = "idle"
	// StateStopped 已停止（生命周期边界态）。
	StateStopped State = "stopped"
)

// transitions 定义合法状态迁移表。
// 语义：from -> to 是否允许。非法迁移一律返回错误。
var transitions = map[State]map[State]bool{
	StatePending: {
		StateStarting: true,
		StateStopped:  true,
	},
	StateStarting: {
		StateWorking: true,
		StateIdle:    true,
		StateStopped: true,
	},
	StateWorking: {
		StateBlocked: true,
		StateDone:    true,
		StateIdle:    true,
		StateStopped: true,
	},
	StateBlocked: {
		StateWorking: true,
		StateDone:    true,
		StateStopped: true,
	},
	StateIdle: {
		StateWorking: true,
		StateDone:    true,
		StateStopped: true,
	},
	StateDone: {
		StateStopped: true,
	},
	StateStopped: {}, // 终态，不可再迁移
}

// Valid 报告 s 是否为合法状态值。
func Valid(s State) bool {
	_, ok := transitions[s]
	return ok
}

// ValidRunMode 报告 mode 是否为合法运行模式。
func ValidRunMode(mode RunMode) bool {
	return mode == RunModeInteractive || mode == RunModeOneshot
}

// CanTransition 报告 from -> to 是否合法。
func CanTransition(from, to State) bool {
	m, ok := transitions[from]
	if !ok {
		return false
	}
	return m[to]
}

var (
	// ErrInvalidTransition 表示状态迁移不合法。
	ErrInvalidTransition = errors.New("agent: invalid state transition")
	// ErrStaleTransitionPlan 表示计划基于的状态版本已过期或不属于目标 Agent。
	ErrStaleTransitionPlan = errors.New("agent: stale transition plan")
)

// TransitionPlan 是持久化前生成、提交后应用的状态迁移计划。
type TransitionPlan struct {
	AgentID  ID
	Revision uint64
	From     State
	To       State
	Reason   string
}

// Agent 是一个受控的 agent 实例。
// 它只描述状态与元数据，不持有进程/PTY/事件实现。
type Agent struct {
	mu sync.RWMutex

	id        ID
	name      string
	vendor    string // 适配器厂商标识，如 "claude" / "codex" / "generic"
	runMode   RunMode
	state     State
	lastError string

	createdAt time.Time
	updatedAt time.Time
	revision  uint64
}

// Option 是 Agent 的构建选项。
type Option func(*Agent)

// RestoreSnapshot 是从持久化事件投影出的 Agent 状态。
type RestoreSnapshot struct {
	ID        ID
	Name      string
	Vendor    string
	RunMode   RunMode
	State     State
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// WithName 设置 agent 显示名。
func WithName(name string) Option {
	return func(a *Agent) { a.name = name }
}

// WithVendor 设置厂商标识。
func WithVendor(vendor string) Option {
	return func(a *Agent) { a.vendor = vendor }
}

// WithRunMode 设置 agent 运行模式。
func WithRunMode(mode RunMode) Option {
	return func(a *Agent) { a.runMode = mode }
}

// New 创建处于 StatePending 的 agent。
func New(id ID, opts ...Option) *Agent {
	a := &Agent{
		id:        id,
		runMode:   RunModeInteractive,
		state:     StatePending,
		createdAt: time.Now().UTC(),
		updatedAt: time.Now().UTC(),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Restore 从已验证的持久化快照构造 Agent，不触发状态变更回调。
func Restore(snapshot RestoreSnapshot, opts ...Option) (*Agent, error) {
	a := &Agent{
		id:        snapshot.ID,
		name:      snapshot.Name,
		vendor:    snapshot.Vendor,
		runMode:   snapshot.RunMode,
		state:     snapshot.State,
		lastError: snapshot.LastError,
		createdAt: snapshot.CreatedAt,
		updatedAt: snapshot.UpdatedAt,
	}
	for _, option := range opts {
		option(a)
	}
	if err := validateRestoredAgent(a); err != nil {
		return nil, err
	}
	return a, nil
}

func validateRestoredAgent(a *Agent) error {
	if strings.TrimSpace(string(a.id)) == "" {
		return errors.New("agent: restore: ID is required")
	}
	if strings.TrimSpace(a.name) == "" {
		return errors.New("agent: restore: name is required")
	}
	if strings.TrimSpace(a.vendor) == "" {
		return errors.New("agent: restore: vendor is required")
	}
	if !ValidRunMode(a.runMode) {
		return fmt.Errorf("agent: restore: invalid run mode %q", a.runMode)
	}
	if !Valid(a.state) {
		return fmt.Errorf("agent: restore: invalid state %q", a.state)
	}
	if a.createdAt.IsZero() {
		return errors.New("agent: restore: creation time is required")
	}
	if a.updatedAt.IsZero() {
		return errors.New("agent: restore: update time is required")
	}
	if a.updatedAt.Before(a.createdAt) {
		return errors.New("agent: restore: update time is before creation time")
	}
	return nil
}

// ID 返回 agent 标识。
func (a *Agent) ID() ID { return a.id }

// Name 返回显示名。
func (a *Agent) Name() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.name
}

// Vendor 返回厂商标识。
func (a *Agent) Vendor() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.vendor
}

// RunMode 返回 agent 的运行模式。
func (a *Agent) RunMode() RunMode {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.runMode
}

// State 返回当前状态（读取权威入口）。
func (a *Agent) State() State {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state
}

// LastError 返回最近一次错误描述（无则为空）。
func (a *Agent) LastError() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lastError
}

// PlanTransition 验证并生成不会立即改变状态的迁移计划。
func (a *Agent) PlanTransition(to State, reason string) (TransitionPlan, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !CanTransition(a.state, to) {
		return TransitionPlan{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, a.state, to)
	}
	return TransitionPlan{
		AgentID:  a.id,
		Revision: a.revision,
		From:     a.state,
		To:       to,
		Reason:   reason,
	}, nil
}

// ValidateTransitionPlan 确认计划仍基于当前权威状态。
func (a *Agent) ValidateTransitionPlan(plan TransitionPlan) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.validateTransitionPlan(plan)
}

// ApplyTransition 在事件已持久化后应用计划。
func (a *Agent) ApplyTransition(plan TransitionPlan, at time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.validateTransitionPlan(plan); err != nil {
		return err
	}
	if at.IsZero() {
		return errors.New("agent: transition time is required")
	}
	a.state = plan.To
	a.updatedAt = at
	a.revision++
	return nil
}

func (a *Agent) validateTransitionPlan(plan TransitionPlan) error {
	if plan.AgentID != a.id || plan.Revision != a.revision || plan.From != a.state {
		return fmt.Errorf(
			"%w: agent=%q revision=%d state=%s",
			ErrStaleTransitionPlan,
			a.id,
			a.revision,
			a.state,
		)
	}
	if !CanTransition(plan.From, plan.To) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, plan.From, plan.To)
	}
	return nil
}

// Transition 立即应用一个迁移，供状态机的独立使用者调用。
func (a *Agent) Transition(to State, reason string) error {
	plan, err := a.PlanTransition(to, reason)
	if err != nil {
		return err
	}
	return a.ApplyTransition(plan, time.Now().UTC())
}

// SetError 记录 agent 的错误信息（不改变状态）。
func (a *Agent) SetError(errMsg string) {
	a.SetErrorAt(errMsg, time.Now().UTC())
}

// SetErrorAt 在事件提交后使用同一时间记录错误信息。
func (a *Agent) SetErrorAt(errMsg string, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastError = errMsg
	a.updatedAt = at
}

// CreatedAt / UpdatedAt 返回时间戳。
func (a *Agent) CreatedAt() time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.createdAt
}

func (a *Agent) UpdatedAt() time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.updatedAt
}
