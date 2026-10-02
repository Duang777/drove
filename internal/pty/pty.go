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
	// OnOutput 在每行输出可用时被调用。回调不得长期阻塞。
	OnOutput func(line string)
	// OnExit 在进程退出后被调用一次。
	OnExit func(info ExitInfo)
}

// Session 是一个运行中的 PTY 会话。
// 用法：Start -> Write（注入输入）-> Close / Wait。
type Session struct {
	mu        sync.Mutex
	closeOnce sync.Once
	closeErr  error

	cmd    *exec.Cmd
	ptmx   *os.File
	closed bool

	onOutput func(line string)
	onExit   func(info ExitInfo)
	readDone chan struct{}
	done     chan struct{}

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
		cmd:      cmd,
		ptmx:     ptmx,
		onOutput: cfg.OnOutput,
		onExit:   cfg.OnExit,
		readDone: make(chan struct{}),
		done:     make(chan struct{}),
		WaitCh:   make(chan ExitInfo, 1),
	}

	// 设置终端行数/列数（默认 120x40，可被上层调整）。
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120})

	go s.readLoop()
	go s.waitLoop()
	return s, nil
}

// readLoop 持续读取 PTY 输出并按行回调。Close 关闭 ptmx 后可中断。
func (s *Session) readLoop() {
	defer close(s.readDone)

	r := bufio.NewReader(s.ptmx)
	for {
		line, err := r.ReadString('\n')
		if line != "" && s.onOutput != nil {
			s.onOutput(line)
		}
		if err != nil {
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
	if s.onExit != nil {
		s.onExit(info)
	}
	select {
	case s.WaitCh <- info:
	default:
	}
	<-s.readDone
	s.closeAfterNaturalExit()
	close(s.done)
}

func (s *Session) closeAfterNaturalExit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if err := s.ptmx.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		s.closeErr = errors.Join(s.closeErr, fmt.Errorf("pty: close master after exit: %w", err))
	}
}

// Write 向 agent 注入完整输入，或返回已写入的字节数和错误。
func (s *Session) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	n, err := writeFull(s.ptmx, data)
	if err != nil {
		return n, fmt.Errorf("pty: write: %w", err)
	}
	return n, nil
}

func writeFull(w io.Writer, data []byte) (int, error) {
	written := 0
	for written < len(data) {
		n, err := w.Write(data[written:])
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
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
	s.closeOnce.Do(func() {
		s.mu.Lock()
		alreadyClosed := s.closed
		s.closed = true
		var closeErr error
		var killErr error
		if !alreadyClosed {
			closeErr = s.ptmx.Close()
			if s.cmd.Process != nil {
				killErr = s.cmd.Process.Kill()
			}
		}
		s.mu.Unlock()

		<-s.done

		if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			s.closeErr = errors.Join(s.closeErr, fmt.Errorf("pty: close master: %w", closeErr))
		}
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			s.closeErr = errors.Join(s.closeErr, fmt.Errorf("pty: kill process: %w", killErr))
		}
	})
	return s.closeErr
}
