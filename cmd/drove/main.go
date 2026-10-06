// Command drove 是 Drove 的 CLI 主程序（daemon 客户端模式）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/cliattach"
	"github.com/Duang777/drove/internal/client"
	"github.com/Duang777/drove/internal/clitui"
	"github.com/Duang777/drove/internal/config"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/term"
	"github.com/Duang777/drove/internal/version"
	"github.com/Duang777/drove/internal/workspace"
)

// exitCodes 语义化退出码。
const (
	exitOK    = 0
	exitUsage = 1
	exitErr   = 2
)

type commandUsageError struct {
	cause error
}

func (e *commandUsageError) Error() string {
	return e.cause.Error()
}

func (e *commandUsageError) Unwrap() error {
	return e.cause
}

func main() {
	// CLI 静默日志，避免污染输出。
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	ctx, stop := newRootContext()
	defer stop()
	root := newRootCmd()
	root.SetContext(ctx)
	if err := executeRoot(root, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "drove:", err)
		os.Exit(commandExitCode(err))
	}
}

func newRootContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
}

func executeRoot(root *cobra.Command, args []string) error {
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	if err := validateDefaultHelpArgs(root, args); err != nil {
		return err
	}
	if !isCompletionRequest(args) {
		command, remaining, err := root.Find(args)
		if err != nil {
			return markUsageError(err)
		}
		afterTerminator := len(remaining) > 0 && remaining[0] == "--"
		if len(remaining) > 0 && remaining[0] == "--" {
			remaining = remaining[1:]
		}
		if command.HasSubCommands() &&
			!command.Runnable() &&
			len(remaining) > 0 &&
			(afterTerminator || !strings.HasPrefix(remaining[0], "-")) {
			return markUsageError(fmt.Errorf(
				"unknown command %q for %q",
				remaining[0],
				command.CommandPath(),
			))
		}
	}
	root.SetArgs(args)
	err := root.Execute()
	if err != nil && len(args) > 0 && args[0] == "completion" {
		return markUsageError(err)
	}
	return err
}

func validateDefaultHelpArgs(root *cobra.Command, args []string) error {
	if len(args) < 2 || args[0] != "help" {
		return nil
	}
	topic := make([]string, 0, len(args)-1)
	afterTerminator := false
	for _, argument := range args[1:] {
		if afterTerminator {
			topic = append(topic, argument)
			continue
		}
		if argument == "--" {
			afterTerminator = true
			continue
		}
		if argument == "-h" || argument == "--help" {
			return nil
		}
		if strings.HasPrefix(argument, "-") {
			continue
		}
		topic = append(topic, argument)
	}
	if len(topic) == 0 {
		return nil
	}
	_, remaining, err := root.Find(topic)
	if err == nil && len(remaining) == 0 {
		return nil
	}
	return markUsageError(fmt.Errorf(
		"unknown help topic %q",
		strings.Join(topic, " "),
	))
}

func isCompletionRequest(args []string) bool {
	return len(args) > 0 &&
		(args[0] == "__complete" || args[0] == "__completeNoDesc")
}

func commandExitCode(err error) int {
	if err == nil {
		return exitOK
	}
	var usageErr *commandUsageError
	if errors.As(err, &usageErr) || client.IsUserError(err) {
		return exitUsage
	}
	return exitErr
}

func markUsageError(err error) error {
	if err == nil {
		return nil
	}
	var usageErr *commandUsageError
	if errors.As(err, &usageErr) {
		return err
	}
	return &commandUsageError{cause: err}
}

func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		return markUsageError(validate(cmd, args))
	}
}

// newRootCmd 组装命令树。
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "drove",
		Short:         "Drove — 跨厂商 Agent 指挥台",
		Long:          "herdr 让 Agent 活着，Drove 让它们往对的方向跑。",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return markUsageError(err)
	})
	root.AddCommand(
		newInitCmd(),
		newUpCmd(),
		newResumeCmd(),
		newPSCmd(),
		newLogCmd(),
		newTimelineCmd(),
		newExplainCmd(),
		newSendCmd(),
		newAttachCmd(),
		newTUICmd(),
		newHookCmd(),
		newStopCmd(),
		newWorktreeCmd(),
		newTokenCmd(),
		newWebCmd(),
		newVersionCmd(),
	)
	return root
}

// newClient 加载配置并返回已确保 daemon 可用的客户端。
func newClient(ctx context.Context) (*client.Client, error) {
	cfg, configPath, err := config.LoadResolved("")
	if err != nil {
		return nil, err
	}
	c := client.NewLocal(
		cfg.DataDir,
		client.WithTokenFile(auth.TokenPath(cfg.DataDir)),
	)
	if err := c.EnsureDaemon(ctx, configPath); err != nil {
		return nil, err
	}
	return c, nil
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "初始化配置与数据目录",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg := config.Defaults()
			path := config.DefaultPath()
			dir := filepath.Dir(path)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			raw, _ := json.MarshalIndent(cfg, "", "  ")
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				return err
			}
			fmt.Printf("Drove initialized: %s\n", path)
			return nil
		},
	}
}

func newUpCmd() *cobra.Command {
	var name string
	var dir string
	var oneshot bool
	var hooks string
	var useWorktree bool
	var branch string
	cmd := &cobra.Command{
		Use:   "up <vendor|command>",
		Short: "启动一个 Agent 会话（自动拉起 daemon）",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			hookPolicy := agent.HookPolicy(hooks)
			if hookPolicy != "" && !agent.ValidHookPolicy(hookPolicy) {
				return markUsageError(
					fmt.Errorf("%w: %q", session.ErrInvalidHookPolicy, hooks),
				)
			}
			if branch != "" && !useWorktree {
				return markUsageError(
					errors.New("--branch requires --worktree"),
				)
			}
			if useWorktree {
				var err error
				dir, err = resolveWorktreeSource(dir)
				if err != nil {
					return err
				}
			}
			ctx := cmd.Context()
			c, err := newClient(ctx)
			if err != nil {
				return err
			}
			st, err := c.Start(
				ctx,
				sessionStartRequest(
					args[0],
					name,
					dir,
					oneshot,
					hookPolicy,
					useWorktree,
					branch,
				),
			)
			if err != nil {
				return err
			}
			fmt.Printf("started agent %s (vendor=%s, mode=%s, state=%s, pid=%d)\n",
				st.AgentID, st.Vendor, st.Mode, st.State, st.PID)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "agent 显示名")
	cmd.Flags().StringVar(&dir, "dir", "", "agent 工作目录")
	cmd.Flags().BoolVar(&oneshot, "oneshot", false, "以单次执行模式启动 agent")
	cmd.Flags().StringVar(&hooks, "hooks", "", "hook 策略（off、auto 或 required）")
	cmd.Flags().BoolVar(&useWorktree, "worktree", false, "在独立 Git worktree 中启动 agent")
	cmd.Flags().StringVar(&branch, "branch", "", "worktree 使用的本地分支")
	return cmd
}

func newPSCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "列出全部 Agent 会话",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := newClient(ctx)
			if err != nil {
				return err
			}
			list, err := c.List(ctx)
			if err != nil {
				return err
			}
			return writeStatuses(cmd.OutOrStdout(), list)
		},
	}
}

func writeStatuses(w io.Writer, list []*session.Status) error {
	if len(list) == 0 {
		_, err := fmt.Fprintln(w, "no agents running")
		return err
	}
	if _, err := fmt.Fprintf(
		w,
		"%-38s %-16s %-10s %-12s %-10s %-6s %-9s\n",
		"AGENT ID",
		"NAME",
		"VENDOR",
		"MODE",
		"STATE",
		"PID",
		"RESUMABLE",
	); err != nil {
		return fmt.Errorf("write status header: %w", err)
	}
	for _, status := range list {
		if _, err := fmt.Fprintf(
			w,
			"%-38s %-16s %-10s %-12s %-10s %-6d %-9t\n",
			status.AgentID,
			truncate(status.Name, 16),
			status.Vendor,
			status.Mode,
			status.State,
			status.PID,
			status.Resumable,
		); err != nil {
			return fmt.Errorf("write status for agent %q: %w", status.AgentID, err)
		}
	}
	return nil
}

func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <agent-id>",
		Short: "原生恢复一个已停止的 Agent 会话",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			status, err := client.Resume(ctx, args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(
				cmd.OutOrStdout(),
				"resumed agent %s (vendor=%s, mode=%s, state=%s, pid=%d)\n",
				status.AgentID,
				status.Vendor,
				status.Mode,
				status.State,
				status.PID,
			)
			return err
		},
	}
}

func newLogCmd() *cobra.Command {
	var plain bool
	cmd := &cobra.Command{
		Use:   "log <agent-id>",
		Short: "回放某 Agent 的事件流",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := newClient(ctx)
			if err != nil {
				return err
			}
			rows, err := c.Replay(ctx, args[0])
			if err != nil {
				return err
			}
			return writeLogRows(cmd.OutOrStdout(), rows, plain)
		},
	}
	cmd.Flags().BoolVar(&plain, "plain", false, "移除终端控制序列")
	return cmd
}

func newTimelineCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "timeline <agent-id>",
		Short: "显示 Agent 状态时间线与 Blocked 索引",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := newClient(ctx)
			if err != nil {
				return err
			}
			timeline, err := c.Timeline(ctx, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(timeline); err != nil {
					return fmt.Errorf("encode timeline: %w", err)
				}
				return nil
			}
			return writeTimeline(cmd.OutOrStdout(), *timeline)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	return cmd
}

func writeTimeline(w io.Writer, timeline recording.Timeline) error {
	if _, err := fmt.Fprintf(
		w,
		"agent %s captured=%s/%s duration=%s output=[%s,%s)\n",
		timeline.AgentID,
		timeline.Captured.Seq,
		timeline.Captured.NextOffset,
		time.Duration(timeline.DurationMillis)*time.Millisecond,
		timeline.Output.Range.Start,
		timeline.Output.Range.End,
	); err != nil {
		return fmt.Errorf("write timeline header: %w", err)
	}
	for _, span := range timeline.Spans {
		end := "live"
		if span.End != nil {
			end = span.End.Seq.String() + "/" + span.End.NextOffset.String()
		}
		if _, err := fmt.Fprintf(
			w,
			"%s start=%s/%s at=%s end=%s source=%s rule=%s reason=%q\n",
			span.State,
			span.Start.Seq,
			span.Start.NextOffset,
			span.StartAt.UTC().Format(time.RFC3339Nano),
			end,
			valueOrDash(span.Source),
			valueOrDash(span.Rule),
			span.Reason,
		); err != nil {
			return fmt.Errorf("write %s timeline span: %w", span.State, err)
		}
	}
	for _, occurrence := range timeline.Blocked {
		duration := "live"
		if occurrence.Span.DurationMillis != nil {
			duration = (time.Duration(*occurrence.Span.DurationMillis) * time.Millisecond).String()
		}
		if _, err := fmt.Fprintf(
			w,
			"blocked #%d start=%s/%s duration=%s jump=%s/%s frame_available=%t\n",
			occurrence.Number,
			occurrence.Span.Start.Seq,
			occurrence.Span.Start.NextOffset,
			duration,
			occurrence.Jump.Seq,
			occurrence.Jump.NextOffset,
			occurrence.FrameAvailable,
		); err != nil {
			return fmt.Errorf(
				"write Blocked occurrence %d: %w",
				occurrence.Number,
				err,
			)
		}
	}
	return nil
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func newExplainCmd() *cobra.Command {
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "explain <agent-id>",
		Short: "解释 Agent 当前状态与最近决策",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("limit") &&
				(limit <= 0 || limit > session.MaxExplainLimit) {
				return markUsageError(fmt.Errorf(
					"%w: must be between 1 and %d",
					session.ErrInvalidExplainLimit,
					session.MaxExplainLimit,
				))
			}
			ctx := cmd.Context()
			c, err := newClient(ctx)
			if err != nil {
				return err
			}
			explanation, err := c.Explain(
				ctx,
				args[0],
				session.ExplainOptions{Limit: limit},
			)
			if err != nil {
				return err
			}
			if asJSON {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(explanation); err != nil {
					return fmt.Errorf("encode explanation: %w", err)
				}
				return nil
			}
			return writeExplanation(cmd.OutOrStdout(), *explanation)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "最近决策数量（默认 50，最大 200）")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	return cmd
}

func writeExplanation(w io.Writer, explanation session.Explanation) error {
	if _, err := fmt.Fprintf(
		w,
		"agent %s state=%s hook_status=%s attached=%t\n",
		explanation.AgentID,
		explanation.State,
		explanation.HookStatus,
		explanation.Attached,
	); err != nil {
		return fmt.Errorf("write explanation header: %w", err)
	}
	for _, item := range explanation.Events {
		var line strings.Builder
		fmt.Fprintf(
			&line,
			"%d %s %s",
			item.Seq,
			item.Timestamp.UTC().Format(time.RFC3339Nano),
			item.Type,
		)
		if item.Source != "" {
			fmt.Fprintf(&line, " source=%s", item.Source)
		}
		if item.Kind != "" {
			fmt.Fprintf(&line, " kind=%s", item.Kind)
		}
		if item.Outcome != "" {
			fmt.Fprintf(&line, " outcome=%s", item.Outcome)
		}
		if item.Rule != "" {
			fmt.Fprintf(&line, " rule=%s", item.Rule)
		}
		if item.Edge != "" {
			fmt.Fprintf(&line, " edge=%s", item.Edge)
		}
		if item.Region != "" {
			fmt.Fprintf(&line, " region=%s", item.Region)
		}
		if item.Evidence != "" {
			fmt.Fprintf(&line, " evidence=%q", item.Evidence)
		}
		if item.SuppressionReason != "" {
			fmt.Fprintf(
				&line,
				" suppression_reason=%q",
				item.SuppressionReason,
			)
		}
		if item.From != "" || item.To != "" {
			fmt.Fprintf(&line, " transition=%s->%s", item.From, item.To)
		}
		if item.Reason != "" {
			fmt.Fprintf(&line, " reason=%q", item.Reason)
		}
		if item.UnsupportedVersion != nil {
			fmt.Fprintf(
				&line,
				" unsupported_version=%d",
				*item.UnsupportedVersion,
			)
		}
		if _, err := fmt.Fprintln(w, line.String()); err != nil {
			return fmt.Errorf("write explanation event at seq %d: %w", item.Seq, err)
		}
	}
	if explanation.Screen == nil {
		return nil
	}
	if _, err := fmt.Fprintln(w, "ephemeral redacted current screen"); err != nil {
		return fmt.Errorf("write explanation screen label: %w", err)
	}
	if _, err := fmt.Fprintf(
		w,
		"captured_at=%s truncated=%t\n",
		explanation.Screen.CapturedAt.UTC().Format(time.RFC3339Nano),
		explanation.Screen.Truncated,
	); err != nil {
		return fmt.Errorf("write explanation screen metadata: %w", err)
	}
	for _, row := range explanation.Screen.Rows {
		if _, err := fmt.Fprintln(w, row); err != nil {
			return fmt.Errorf("write explanation screen row: %w", err)
		}
	}
	return nil
}

func writeLogRows(w io.Writer, rows []store.EventRow, plain bool) error {
	var stripper term.Stripper
	writeOutput := func(seq uint64, data []byte) error {
		if plain {
			data = stripper.Feed(data)
		}
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("write output at seq %d: %w", seq, err)
		}
		return nil
	}
	for _, row := range rows {
		switch event.Type(row.Type) {
		case event.TypeOutput:
			data := []byte(strings.TrimRight(row.Payload, "\r\n") + "\n")
			if err := writeOutput(row.Seq, data); err != nil {
				return err
			}
		case event.TypeOutputChunk:
			payload, err := event.DecodeOutputChunkPayload(row.Payload)
			if err != nil {
				return fmt.Errorf("decode output chunk at seq %d: %w", row.Seq, err)
			}
			if payload.DataB64 == "" {
				continue
			}
			data, err := payload.DecodeData()
			if err != nil {
				return fmt.Errorf("decode output chunk data at seq %d: %w", row.Seq, err)
			}
			if err := writeOutput(row.Seq, data); err != nil {
				return err
			}
		}
	}
	if plain {
		if _, err := w.Write(stripper.Flush()); err != nil {
			return fmt.Errorf("write final plain output: %w", err)
		}
	}
	return nil
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <agent-id>",
		Short: "停止一个 Agent 会话",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := newClient(ctx)
			if err != nil {
				return err
			}
			if err := c.Stop(ctx, args[0]); err != nil {
				return err
			}
			fmt.Println("stopped", args[0])
			return nil
		},
	}
}

func newWorktreeCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "worktree",
		Short: "管理 Drove 创建的 Git worktree",
	}
	command.AddCommand(newWorktreeListCmd(), newWorktreeRemoveCmd())
	return command
}

func newWorktreeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "列出 Drove 创建的 Git worktree",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, err := newWorkspaceManager()
			if err != nil {
				return err
			}
			worktrees, err := manager.List(cmd.Context())
			if err != nil {
				return err
			}
			return writeWorktrees(cmd.OutOrStdout(), worktrees)
		},
	}
}

func newWorktreeRemoveCmd() *cobra.Command {
	var force bool
	command := &cobra.Command{
		Use:   "rm <agent-id>",
		Short: "删除一个 Drove worktree，保留其分支",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			daemonClient, err := newClient(ctx)
			if err != nil {
				return err
			}
			removed, err := daemonClient.CleanupWorktree(ctx, args[0], force)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(
				cmd.OutOrStdout(),
				"removed worktree %s (branch=%s preserved)\n",
				removed.AgentID,
				removed.Branch,
			)
			return err
		},
	}
	command.Flags().BoolVar(
		&force,
		"force",
		false,
		"强制删除，允许丢弃未提交更改或 detached HEAD，并绕过旧版未知保护信息检查",
	)
	return command
}

func newWorkspaceManager() (*workspace.Manager, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	return workspace.New(cfg.DataDir)
}

func writeWorktrees(w io.Writer, worktrees []workspace.Workspace) error {
	if len(worktrees) == 0 {
		_, err := fmt.Fprintln(w, "no managed worktrees")
		return err
	}
	if _, err := fmt.Fprintf(
		w,
		"%-38s %-30s %-5s %s\n",
		"AGENT ID",
		"BRANCH",
		"DIRTY",
		"PATH",
	); err != nil {
		return fmt.Errorf("write worktree header: %w", err)
	}
	for _, current := range worktrees {
		if _, err := fmt.Fprintf(
			w,
			"%-38s %-30s %-5t %s\n",
			current.AgentID,
			current.Branch,
			current.Dirty,
			current.Path,
		); err != nil {
			return fmt.Errorf(
				"write worktree for agent %q: %w",
				current.AgentID,
				err,
			)
		}
	}
	return nil
}

func newTokenCmd() *cobra.Command {
	token := &cobra.Command{
		Use:   "token",
		Short: "管理本地控制令牌",
	}
	token.AddCommand(&cobra.Command{
		Use:   "rotate",
		Short: "轮换本地控制令牌",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newClient(cmd.Context())
			if err != nil {
				return err
			}
			if err := c.RotateToken(cmd.Context()); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "control token rotated")
			return err
		},
	})
	return token
}

var launchBrowser = openBrowser

func newWebCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "web",
		Short: "打开 Web 控制台",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, configPath, err := config.LoadResolved("")
			if err != nil {
				return err
			}
			if cfg.DisableTCP {
				return errors.New("web console is unavailable while disable_tcp is true")
			}
			c := client.NewLocal(
				cfg.DataDir,
				client.WithTokenFile(auth.TokenPath(cfg.DataDir)),
			)
			if err := c.EnsureDaemon(cmd.Context(), configPath); err != nil {
				return err
			}
			code, err := c.IssueLoginCode(cmd.Context())
			if err != nil {
				return err
			}
			browserURL, err := newBrowserURL(cfg.APIBind, code)
			if err != nil {
				return err
			}
			if err := launchBrowser(browserURL.login); err != nil {
				return fmt.Errorf("open browser: %w", err)
			}
			_, err = fmt.Fprintf(
				cmd.OutOrStdout(),
				"opened Drove console at %s\n",
				browserURL.public,
			)
			return err
		},
	}
}

type browserURL struct {
	login  string
	public string
}

func newBrowserURL(address, code string) (browserURL, error) {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return browserURL{}, fmt.Errorf("invalid browser address %q: %w", address, err)
	}
	if code == "" {
		return browserURL{}, errors.New("browser login code is empty")
	}
	loginURL := url.URL{
		Scheme:   "http",
		Host:     address,
		Path:     "/login",
		Fragment: code,
	}
	publicURL := loginURL
	publicURL.Fragment = ""
	return browserURL{
		login:  loginURL.String(),
		public: publicURL.String(),
	}, nil
}

func openBrowser(rawURL string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
		args = []string{rawURL}
	case "linux":
		command = "xdg-open"
		args = []string{rawURL}
	case "windows":
		command = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", rawURL}
	default:
		return fmt.Errorf("unsupported platform %q", runtime.GOOS)
	}
	process := exec.Command(command, args...)
	if err := process.Start(); err != nil {
		return err
	}
	go func() {
		_ = process.Wait()
	}()
	return nil
}

func newSendCmd() *cobra.Command {
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "send <agent-id> [text]",
		Short: "向运行中的 Agent 发送输入",
		Args: usageArgs(func(_ *cobra.Command, args []string) error {
			if fromStdin {
				if len(args) != 1 {
					return errors.New("send with --stdin requires exactly one agent ID")
				}
				return nil
			}
			if len(args) != 2 {
				return errors.New("send requires an agent ID and text, or --stdin")
			}
			return nil
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readSendInput(
				cmd.Context(),
				args,
				fromStdin,
				cmd.InOrStdin(),
			)
			if err != nil {
				if errors.Is(err, session.ErrInputEmpty) ||
					errors.Is(err, session.ErrInputTooLarge) ||
					errors.Is(err, session.ErrInputNotUTF8) {
					return markUsageError(err)
				}
				return err
			}
			ctx := cmd.Context()
			c, err := newClient(ctx)
			if err != nil {
				return err
			}
			if err := c.SendInput(ctx, args[0], data); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "sent %d bytes to %s\n", len(data), args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "从标准输入读取内容，不自动添加换行")
	return cmd
}

type attachClientFactory func(context.Context) (*client.Client, error)

type attachRunner func(
	context.Context,
	*client.Client,
	string,
	cliattach.Options,
) error

func newAttachCmd() *cobra.Command {
	return newAttachCmdWith(newClient, cliattach.Run)
}

func newAttachCmdWith(
	clientFactory attachClientFactory,
	runner attachRunner,
) *cobra.Command {
	var readOnly bool
	cmd := &cobra.Command{
		Use:   "attach <agent-id>",
		Short: "连接到 Agent 的实时终端",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			daemon, err := clientFactory(cmd.Context())
			if err != nil {
				return err
			}
			return runner(
				cmd.Context(),
				daemon,
				args[0],
				cliattach.Options{ReadOnly: readOnly},
			)
		},
	}
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "只读连接，不发送输入或尺寸")
	return cmd
}

type tuiClientFactory func(context.Context) (*client.Client, error)

type tuiRunner func(context.Context, *client.Client) error

func newTUICmd() *cobra.Command {
	return newTUICmdWith(newClient, clitui.Run)
}

func newTUICmdWith(
	clientFactory tuiClientFactory,
	runner tuiRunner,
) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "打开 Agent 终端总览",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			daemon, err := clientFactory(cmd.Context())
			if err != nil {
				return err
			}
			return runner(cmd.Context(), daemon)
		},
	}
}

func newHookCmd() *cobra.Command {
	var (
		vendor      string
		managedBy   string
		payloadArgv bool
	)
	cmd := &cobra.Command{
		Use:   "hook [payload]",
		Short: "转发一个厂商 hook 事件",
		Args: usageArgs(func(_ *cobra.Command, args []string) error {
			if vendor == "" {
				return errors.New("hook requires --vendor")
			}
			if managedBy != "" && managedBy != "drove/v1" {
				return errors.New("hook --managed-by must be drove/v1")
			}
			if payloadArgv {
				if len(args) != 1 {
					return errors.New("hook --payload-argv requires exactly one payload")
				}
				return nil
			}
			if len(args) != 0 {
				return errors.New("hook reads stdin unless --payload-argv is set")
			}
			return nil
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := forwardHook(cmd, vendor, payloadArgv, args); err != nil {
				_, _ = fmt.Fprintln(
					cmd.ErrOrStderr(),
					"drove hook: signal delivery failed",
				)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&vendor, "vendor", "", "hook 厂商（claude 或 codex）")
	cmd.Flags().StringVar(&managedBy, "managed-by", "", "受管命令标识")
	cmd.Flags().BoolVar(&payloadArgv, "payload-argv", false, "从唯一位置参数读取 JSON")
	_ = cmd.MarkFlagRequired("vendor")
	return cmd
}

func forwardHook(
	cmd *cobra.Command,
	vendor string,
	payloadArgv bool,
	args []string,
) error {
	relay, err := client.NewHookRelay(client.HookRelayConfig{
		AgentID:    os.Getenv(session.SignalAgentIDEnv),
		SignalURL:  os.Getenv(session.SignalURLEnv),
		SocketPath: os.Getenv(session.SignalSocketEnv),
		Token:      os.Getenv(session.SignalTokenEnv),
	})
	if err != nil {
		return err
	}

	var payload []byte
	if payloadArgv {
		payload = []byte(args[0])
	} else {
		payload, err = readAllWithContext(
			cmd.Context(),
			cmd.InOrStdin(),
			client.MaxHookPayloadBytes+1,
		)
		if err != nil {
			return fmt.Errorf("read hook payload: %w", err)
		}
	}
	if len(payload) > client.MaxHookPayloadBytes {
		return fmt.Errorf(
			"hook payload exceeds %d bytes",
			client.MaxHookPayloadBytes,
		)
	}
	return relay.Forward(cmd.Context(), vendor, payload)
}

func readSendInput(
	ctx context.Context,
	args []string,
	fromStdin bool,
	stdin io.Reader,
) ([]byte, error) {
	var data []byte
	if fromStdin {
		if len(args) != 1 {
			return nil, errors.New("send with --stdin requires exactly one agent ID")
		}
		var err error
		data, err = readAllWithContext(ctx, stdin, session.MaxInputBytes+1)
		if err != nil {
			return nil, fmt.Errorf("read input: %w", err)
		}
	} else {
		if len(args) != 2 {
			return nil, errors.New("send requires an agent ID and text, or --stdin")
		}
		data = []byte(args[1] + "\n")
	}

	switch {
	case len(data) == 0:
		return nil, session.ErrInputEmpty
	case len(data) > session.MaxInputBytes:
		return nil, fmt.Errorf("%w: %d bytes", session.ErrInputTooLarge, len(data))
	case !utf8.Valid(data):
		return nil, session.ErrInputNotUTF8
	default:
		return data, nil
	}
}

func readAllWithContext(
	ctx context.Context,
	reader io.Reader,
	limit int64,
) ([]byte, error) {
	stopClose := func() bool {
		return false
	}
	if closer, ok := reader.(io.Closer); ok {
		stopClose = context.AfterFunc(ctx, func() {
			_ = closer.Close()
		})
	}
	defer stopClose()

	data, err := io.ReadAll(io.LimitReader(reader, limit))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return data, err
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "输出版本信息",
		Args:  usageArgs(cobra.NoArgs),
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("drove %s (commit=%s, built=%s)\n", version.Version, version.Commit, version.Date)
		},
	}
}

// sessionStartRequest 把 CLI 参数映射为 daemon 的 StartRequest。
// 若第一个参数不是内置厂商名，则视为 generic 命令。
func sessionStartRequest(
	arg string,
	name string,
	dir string,
	oneshot bool,
	hooks agent.HookPolicy,
	useWorktree bool,
	branch string,
) session.StartRequest {
	vendor := arg
	cmdName := ""
	if !isKnownVendor(arg) {
		cmdName, vendor = arg, "generic"
	}
	mode := agent.RunModeInteractive
	if oneshot {
		mode = agent.RunModeOneshot
	}
	request := session.StartRequest{
		Vendor:  vendor,
		Name:    name,
		Command: cmdName,
		Dir:     dir,
		Mode:    mode,
		Hooks:   hooks,
	}
	if useWorktree {
		request.Worktree = &session.WorktreeRequest{Branch: branch}
	}
	return request
}

func resolveWorktreeSource(dir string) (string, error) {
	if dir == "" {
		current, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory for worktree: %w", err)
		}
		dir = current
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve worktree source %q: %w", dir, err)
	}
	return filepath.Clean(absolute), nil
}

func isKnownVendor(s string) bool {
	switch s {
	case "claude", "codex", "generic":
		return true
	}
	return false
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
