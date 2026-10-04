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
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/client"
	"github.com/Duang777/drove/internal/config"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
	"github.com/Duang777/drove/internal/term"
	"github.com/Duang777/drove/internal/version"
)

// exitCodes 语义化退出码。
const (
	exitOK    = 0
	exitUsage = 1
	exitErr   = 2
)

func main() {
	// CLI 静默日志，避免污染输出。
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "drove:", err)
		os.Exit(exitErr)
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
	root.AddCommand(
		newInitCmd(),
		newUpCmd(),
		newPSCmd(),
		newLogCmd(),
		newTimelineCmd(),
		newExplainCmd(),
		newSendCmd(),
		newHookCmd(),
		newStopCmd(),
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
	cmd := &cobra.Command{
		Use:   "up <vendor|command>",
		Short: "启动一个 Agent 会话（自动拉起 daemon）",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			hookPolicy := agent.HookPolicy(hooks)
			if hookPolicy != "" && !agent.ValidHookPolicy(hookPolicy) {
				return fmt.Errorf("%w: %q", session.ErrInvalidHookPolicy, hooks)
			}
			ctx := context.Background()
			c, err := newClient(ctx)
			if err != nil {
				return err
			}
			st, err := c.Start(
				ctx,
				sessionStartRequest(args[0], name, dir, oneshot, hookPolicy),
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
	return cmd
}

func newPSCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "列出全部 Agent 会话",
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx := context.Background()
			c, err := newClient(ctx)
			if err != nil {
				return err
			}
			list, err := c.List(ctx)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println("no agents running")
				return nil
			}
			fmt.Printf("%-38s %-16s %-10s %-12s %-10s %-6s\n", "AGENT ID", "NAME", "VENDOR", "MODE", "STATE", "PID")
			for _, st := range list {
				fmt.Printf("%-38s %-16s %-10s %-12s %-10s %-6d\n",
					st.AgentID, truncate(st.Name, 16), st.Vendor, st.Mode, st.State, st.PID)
			}
			return nil
		},
	}
}

func newLogCmd() *cobra.Command {
	var plain bool
	cmd := &cobra.Command{
		Use:   "log <agent-id>",
		Short: "回放某 Agent 的事件流",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
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
		Args:  cobra.ExactArgs(1),
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
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("limit") &&
				(limit <= 0 || limit > session.MaxExplainLimit) {
				return fmt.Errorf(
					"%w: must be between 1 and %d",
					session.ErrInvalidExplainLimit,
					session.MaxExplainLimit,
				)
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
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ctx := context.Background()
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

func newTokenCmd() *cobra.Command {
	token := &cobra.Command{
		Use:   "token",
		Short: "管理本地控制令牌",
	}
	token.AddCommand(&cobra.Command{
		Use:   "rotate",
		Short: "轮换本地控制令牌",
		Args:  cobra.NoArgs,
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
		Args:  cobra.NoArgs,
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
		Args: func(_ *cobra.Command, args []string) error {
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
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readSendInput(args, fromStdin, cmd.InOrStdin())
			if err != nil {
				return err
			}
			ctx := context.Background()
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

func newHookCmd() *cobra.Command {
	var (
		vendor      string
		managedBy   string
		payloadArgv bool
	)
	cmd := &cobra.Command{
		Use:   "hook [payload]",
		Short: "转发一个厂商 hook 事件",
		Args: func(_ *cobra.Command, args []string) error {
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
		},
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
		payload, err = io.ReadAll(io.LimitReader(
			cmd.InOrStdin(),
			client.MaxHookPayloadBytes+1,
		))
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

func readSendInput(args []string, fromStdin bool, stdin io.Reader) ([]byte, error) {
	var data []byte
	if fromStdin {
		if len(args) != 1 {
			return nil, errors.New("send with --stdin requires exactly one agent ID")
		}
		var err error
		data, err = io.ReadAll(io.LimitReader(stdin, session.MaxInputBytes+1))
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

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "输出版本信息",
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
	return session.StartRequest{
		Vendor:  vendor,
		Name:    name,
		Command: cmdName,
		Dir:     dir,
		Mode:    mode,
		Hooks:   hooks,
	}
}

func isKnownVendor(s string) bool {
	switch s {
	case "claude", "codex", "generic":
		return true
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
