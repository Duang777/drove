// Command drove 是 Drove 的 CLI 主程序（daemon 客户端模式）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/client"
	"github.com/Duang777/drove/internal/config"
	"github.com/Duang777/drove/internal/event"
	"github.com/Duang777/drove/internal/session"
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
		newSendCmd(),
		newStopCmd(),
		newVersionCmd(),
	)
	return root
}

// newClient 加载配置并返回已确保 daemon 可用的客户端。
func newClient(ctx context.Context) (*client.Client, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	c := client.New(cfg.APIBind)
	if err := c.EnsureDaemon(ctx); err != nil {
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
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			dir := filepath.Join(home, ".drove")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			path := filepath.Join(dir, "config.json")
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
	cmd := &cobra.Command{
		Use:   "up <vendor|command>",
		Short: "启动一个 Agent 会话（自动拉起 daemon）",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ctx := context.Background()
			c, err := newClient(ctx)
			if err != nil {
				return err
			}
			st, err := c.Start(ctx, sessionStartRequest(args[0], name, dir, oneshot))
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
	return &cobra.Command{
		Use:   "log <agent-id>",
		Short: "回放某 Agent 的事件流",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ctx := context.Background()
			c, err := newClient(ctx)
			if err != nil {
				return err
			}
			rows, err := c.Replay(ctx, args[0])
			if err != nil {
				return err
			}
			for _, r := range rows {
				ts := r.Timestamp.Format("15:04:05.000")
				if r.Type == string(event.TypeOutput) {
					fmt.Printf("%s %-14s %s", ts, "["+shortID(r.AgentID)+"]", r.Payload)
				} else {
					fmt.Printf("%s %-14s %s: %s\n", ts, "["+shortID(r.AgentID)+"]", r.Type, r.Reason)
				}
			}
			return nil
		},
	}
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
func sessionStartRequest(arg, name, dir string, oneshot bool) session.StartRequest {
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
