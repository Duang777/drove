// Package pty 管理 agent 进程的伪终端生命周期与字节流桥接。
package pty

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// ExitInfo 描述进程退出信息。
type ExitInfo struct {
	PID  int
	Code int
	Err  error
}

// Config 是启动一个 PTY 会话的参数。
type Config struct {
	// Command 是要启动的可执行文件（如 "claude"、"codex"）。
	Command string
	// Args 是命令行参数。
	Args []string
	// Env 是附加环境变量（K=V），会合并到当前环境之上。
	Env []string
	// Dir 是工作目录；空则继承当前目录。
	Dir string
}

// Session 是一个运行中的 PTY 会话。
// 用法：Start -> Write（注入输入）；OnOutput（读取回调）；Close / Wait。
type Session struct {
	mu sync.Mutex

	cmd    *exec.Cmd
	ptmx   *os.File
	closed bool

	// OnOutput 在每行输出可用时被调用（非阻塞约定：回调内不得阻塞）。
	OnOutput func(line string)
	// OnExit 在进程退出后被调用。
	OnExit func(info ExitInfo)

	// WaitCh 返回进程退出信息（Close 后仍可读取一次）。
	WaitCh chan ExitInfo
}

var (
	// ErrClosed 表示会话已关闭。
	ErrClosed = errors.New("pty: session closed")
	// ErrNotStarted 表示会话未启动。
	ErrNotStarted = errors.New("pty: session not started")
)

// Start 创建 PTY 并启动命令。返回会话，或错误。
func Start(cfg Config) (*Session, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("pty: start %q: %w", cfg.Command, err)
	}

	s := &Session{
		cmd:    cmd,
		ptmx:   ptmx,
		WaitCh: make(chan ExitInfo, 1),
	}

	// 设置终端行数/列数（默认 120x40，可被上层调整）。
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120})

	go s.readLoop()
	go s.waitLoop()
	return s, nil
}

// readLoop 持续读取 PTY 输出并按行回调。Close 关闭 ptmx 后可中断。
func (s *Session) readLoop() {
	r := bufio.NewReader(s.ptmx)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			if s.OnOutput != nil {
				s.OnOutput(line)
			}
		}
		if err != nil {
			if err != io.EOF && !errors.Is(err, os.ErrClosed) {
				// 读取异常：仍尝试将残余行上抛后退出。
				if line != "" && s.OnOutput != nil {
					s.OnOutput(line)
				}
			}
			return
		}
	}
}

// waitLoop 等待进程退出并通知。
func (s *Session) waitLoop() {
	info := ExitInfo{PID: s.cmd.Process.Pid}
	err := s.cmd.Wait()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			info.Code = ee.ExitCode()
		}
		info.Err = err
	} else {
		info.Code = 0
	}
	select {
	case s.WaitCh <- info:
	default:
	}
	if s.OnExit != nil {
		s.OnExit(info)
	}
}

// Write 向 agent 注入输入。
func (s *Session) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	n, err := s.ptmx.Write(data)
	if err != nil {
		return n, fmt.Errorf("pty: write: %w", err)
	}
	return n, nil
}

// Resize 调整终端尺寸（rows x cols）。
func (s *Session) Resize(rows, cols uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	return pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

// PID 返回子进程 PID。
func (s *Session) PID() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Close 关闭 PTY，中断读取，并终止子进程（若仍存活）。
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.closed = true

	// 关闭主从端，触发 readLoop 退出。
	err := s.ptmx.Close()

	// 若进程仍在运行则终止。
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	if err != nil && !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("pty: close: %w", err)
	}
	return nil
}
