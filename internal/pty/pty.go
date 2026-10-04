// Package pty 管理 agent 进程的伪终端生命周期与字节流桥接。
package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"unicode/utf8"

	"github.com/creack/pty"
)

const maxTerminalDimension = 1<<16 - 1

// ExitInfo 描述进程退出信息。
type ExitInfo struct {
	PID  int
	Code int
	Err  error
}

// Size 是经过校验的 PTY 行列尺寸。
type Size struct {
	rows    int
	columns int
}

// NewSize 校验并创建 PTY 尺寸。
func NewSize(rows, columns int) (Size, error) {
	size := Size{rows: rows, columns: columns}
	if err := size.validate(); err != nil {
		return Size{}, err
	}
	return size, nil
}

// Rows 返回 PTY 行数。
func (s Size) Rows() int {
	return s.rows
}

// Columns 返回 PTY 列数。
func (s Size) Columns() int {
	return s.columns
}

func (s Size) validate() error {
	if s.rows <= 0 || s.rows > maxTerminalDimension {
		return fmt.Errorf("PTY rows must be between 1 and %d", maxTerminalDimension)
	}
	if s.columns <= 0 || s.columns > maxTerminalDimension {
		return fmt.Errorf("PTY columns must be between 1 and %d", maxTerminalDimension)
	}
	return nil
}

func (s Size) windowSize() (*pty.Winsize, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &pty.Winsize{
		Rows: uint16(s.rows),
		Cols: uint16(s.columns),
	}, nil
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
	// Size 是子进程启动前生效的 PTY 行列尺寸。
	Size Size
	// OnOutput 在输出字节可用时被调用。回调不得长期阻塞。
	OnOutput func(chunk []byte, offset uint64)
	// OnOutputEnd 在最后一次输出回调后被调用一次。
	OnOutputEnd func(offset uint64)
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

	onOutput    func(chunk []byte, offset uint64)
	onOutputEnd func(offset uint64)
	onExit      func(info ExitInfo)
	readDone    chan struct{}
	done        chan struct{}

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
	windowSize, err := cfg.Size.windowSize()
	if err != nil {
		return nil, fmt.Errorf("pty: invalid initial size: %w", err)
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}

	ptmx, err := pty.StartWithSize(cmd, windowSize)
	if err != nil {
		return nil, fmt.Errorf("pty: start %q: %w", cfg.Command, err)
	}

	s := &Session{
		cmd:         cmd,
		ptmx:        ptmx,
		onOutput:    cfg.OnOutput,
		onOutputEnd: cfg.OnOutputEnd,
		onExit:      cfg.OnExit,
		readDone:    make(chan struct{}),
		done:        make(chan struct{}),
		WaitCh:      make(chan ExitInfo, 1),
	}

	go s.readLoop()
	go s.waitLoop()
	return s, nil
}

const outputReadBufferSize = 32 * 1024

// readLoop 持续读取 PTY 输出字节。Close 关闭 ptmx 后可中断。
func (s *Session) readLoop() {
	defer close(s.readDone)
	readOutput(s.ptmx, s.onOutput, s.onOutputEnd)
}

func readOutput(
	reader io.Reader,
	onOutput func([]byte, uint64),
	onOutputEnd func(uint64),
) {
	buffer := make([]byte, outputReadBufferSize)
	var pending []byte
	var offset uint64
	deliver := func(data []byte) {
		if len(data) == 0 {
			return
		}
		chunk := append([]byte(nil), data...)
		if onOutput != nil {
			onOutput(chunk, offset)
		}
		offset += uint64(len(chunk))
	}
	defer func() {
		deliver(pending)
		if onOutputEnd != nil {
			onOutputEnd(offset)
		}
	}()

	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			pending = append(pending, buffer[:n]...)
			for len(pending) > 0 {
				prefix := completeUTF8Prefix(pending, outputReadBufferSize)
				if prefix == 0 {
					break
				}
				deliver(pending[:prefix])
				pending = append([]byte(nil), pending[prefix:]...)
			}
		}
		if err != nil {
			return
		}
	}
}

func completeUTF8Prefix(data []byte, limit int) int {
	if limit > len(data) {
		limit = len(data)
	}
	lastComplete := 0
	for index := 0; index < limit; {
		if !utf8.FullRune(data[index:]) {
			return lastComplete
		}
		r, size := utf8.DecodeRune(data[index:])
		if r != utf8.RuneError || size != 1 {
			if index+size > limit {
				return lastComplete
			}
			index += size
			lastComplete = index
			continue
		}
		index++
		lastComplete = index
	}
	return lastComplete
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
