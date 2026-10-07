// Package cliattach owns the local terminal lifecycle for drove attach.
package cliattach

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	xterm "github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"

	"github.com/Duang777/drove/internal/client"
)

const maxInputBytes = 64 * 1024

var (
	errLocalDetach      = errors.New("cliattach: local detach")
	errStdinNotTerminal = errors.New("cliattach: stdin is not a terminal")
)

// Options configures one CLI terminal attachment.
type Options struct {
	ReadOnly bool
	Stdin    io.Reader
	Stdout   io.Writer
}

type terminalStream interface {
	Subscribe(context.Context, client.TerminalSubscription) error
	Next(context.Context, func(client.TerminalMessage) error) error
	SendInput(context.Context, string, string) (int, error)
	Resize(context.Context, string, int, int) error
	Close() error
}

type inputTerminal interface {
	io.Reader
	Fd() uintptr
}

type cancelReader interface {
	io.ReadCloser
	Cancel() bool
}

type runnerDependencies struct {
	stdin  inputTerminal
	stdout io.Writer

	open            func(context.Context) (terminalStream, error)
	isTerminal      func(uintptr) bool
	makeRaw         func(uintptr) (*xterm.State, error)
	restore         func(uintptr, *xterm.State) error
	getSize         func(uintptr) (int, int, error)
	newCancelReader func(io.Reader) (cancelReader, error)
	notifyResize    func(chan<- os.Signal)
	stopResize      func(chan<- os.Signal)
}

type pumpResult struct {
	name string
	err  error
}

// Run attaches the current terminal to one agent until local detach or EOF.
func Run(
	ctx context.Context,
	daemon *client.Client,
	agentID string,
	options Options,
) error {
	if daemon == nil {
		return errors.New("cliattach: client is required")
	}
	stdin, stdout, err := terminalStreams(options)
	if err != nil {
		return err
	}
	return run(ctx, agentID, options, runnerDependencies{
		stdin:  stdin,
		stdout: stdout,
		open: func(ctx context.Context) (terminalStream, error) {
			return daemon.OpenTerminal(ctx)
		},
		isTerminal: xterm.IsTerminal,
		makeRaw:    xterm.MakeRaw,
		restore:    xterm.Restore,
		getSize: func(fd uintptr) (int, int, error) {
			columns, rows, err := xterm.GetSize(fd)
			return rows, columns, err
		},
		newCancelReader: func(reader io.Reader) (cancelReader, error) {
			return cancelreader.NewReader(reader)
		},
		notifyResize: func(ch chan<- os.Signal) {
			signal.Notify(ch, syscall.SIGWINCH)
		},
		stopResize: signal.Stop,
	})
}

func terminalStreams(options Options) (inputTerminal, io.Writer, error) {
	input := io.Reader(os.Stdin)
	if options.Stdin != nil {
		input = options.Stdin
	}
	stdin, ok := input.(inputTerminal)
	if !ok {
		return nil, nil, errStdinNotTerminal
	}
	stdout := io.Writer(os.Stdout)
	if options.Stdout != nil {
		stdout = options.Stdout
	}
	return stdin, stdout, nil
}

func run(
	ctx context.Context,
	agentID string,
	options Options,
	deps runnerDependencies,
) (retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if agentID == "" {
		return errors.New("cliattach: agent ID is required")
	}
	if deps.stdin == nil || deps.stdout == nil || deps.open == nil {
		return errors.New("cliattach: runner dependencies are incomplete")
	}

	fd := deps.stdin.Fd()
	if !deps.isTerminal(fd) {
		return errStdinNotTerminal
	}
	previousState, err := deps.makeRaw(fd)
	if err != nil {
		return fmt.Errorf("cliattach: enter raw mode: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	var (
		reader           cancelReader
		stream           terminalStream
		resizeSignals    chan os.Signal
		resizeRegistered bool
		shutdownOnce     sync.Once
		shutdownErr      error
	)
	shutdown := func() {
		shutdownOnce.Do(func() {
			cancel()
			if resizeRegistered {
				deps.stopResize(resizeSignals)
			}
			if reader != nil {
				reader.Cancel()
			}
			if stream != nil {
				if err := stream.Close(); err != nil {
					shutdownErr = fmt.Errorf("cliattach: close terminal stream: %w", err)
				}
			}
		})
	}
	defer func() {
		shutdown()
		var releaseErr error
		if reader != nil {
			if err := reader.Close(); err != nil {
				releaseErr = errors.Join(
					releaseErr,
					fmt.Errorf("cliattach: close input reader: %w", err),
				)
			}
		}
		if err := deps.restore(fd, previousState); err != nil {
			releaseErr = errors.Join(
				releaseErr,
				fmt.Errorf("cliattach: restore terminal: %w", err),
			)
		}
		retErr = errors.Join(retErr, shutdownErr, releaseErr)
	}()

	reader, err = deps.newCancelReader(deps.stdin)
	if err != nil {
		return fmt.Errorf("cliattach: create cancelable input: %w", err)
	}
	stream, err = deps.open(runCtx)
	if err != nil {
		return fmt.Errorf("cliattach: open terminal stream: %w", err)
	}

	subscription := client.TerminalSubscription{
		AgentID: agentID,
		Mode:    client.TerminalModeRaw,
		Access:  client.TerminalAccessReadOnly,
	}
	if !options.ReadOnly {
		rows, columns, err := deps.getSize(fd)
		if err != nil {
			return fmt.Errorf("cliattach: read terminal size: %w", err)
		}
		subscription.Access = client.TerminalAccessReadWrite
		subscription.Rows = rows
		subscription.Columns = columns
	}
	if err := stream.Subscribe(runCtx, subscription); err != nil {
		return fmt.Errorf("cliattach: subscribe terminal: %w", err)
	}

	if !options.ReadOnly {
		resizeSignals = make(chan os.Signal, 1)
		deps.notifyResize(resizeSignals)
		resizeRegistered = true
	}

	results := make(chan pumpResult, 3)
	pumpCount := 2
	go func() {
		results <- pumpResult{
			name: "output",
			err:  pumpOutput(runCtx, stream, deps.stdout),
		}
	}()
	go func() {
		results <- pumpResult{
			name: "input",
			err:  pumpInput(runCtx, stream, reader, agentID, !options.ReadOnly),
		}
	}()
	if !options.ReadOnly {
		pumpCount++
		go func() {
			results <- pumpResult{
				name: "resize",
				err: pumpResize(
					runCtx,
					stream,
					resizeSignals,
					agentID,
					fd,
					deps.getSize,
				),
			}
		}()
	}

	collected := make([]pumpResult, 0, pumpCount)
	select {
	case result := <-results:
		collected = append(collected, result)
	case <-ctx.Done():
	}
	for len(collected) < pumpCount {
		select {
		case result := <-results:
			collected = append(collected, result)
		default:
			goto shutdown
		}
	}

shutdown:
	primaryCount := len(collected)
	shutdown()
	for len(collected) < pumpCount {
		collected = append(collected, <-results)
	}

	var pumpErr error
	if ctxErr := ctx.Err(); ctxErr != nil {
		pumpErr = ctxErr
	}
	for _, result := range collected[:primaryCount] {
		if err := significantPumpError(result.err); err != nil {
			pumpErr = errors.Join(
				pumpErr,
				fmt.Errorf("cliattach: %s pump: %w", result.name, err),
			)
		}
	}
	return pumpErr
}

func pumpOutput(
	ctx context.Context,
	stream terminalStream,
	output io.Writer,
) error {
	for {
		err := stream.Next(ctx, func(message client.TerminalMessage) error {
			switch message := message.(type) {
			case client.TerminalOutput:
				return writeAll(output, message.Data)
			case client.TerminalResize, client.TerminalCaughtUp:
				return nil
			default:
				return fmt.Errorf(
					"unexpected terminal message %T",
					message,
				)
			}
		})
		if err != nil {
			return err
		}
	}
}

func pumpInput(
	ctx context.Context,
	stream terminalStream,
	input cancelReader,
	agentID string,
	writable bool,
) error {
	buffer := make([]byte, stdinChunkBytes)
	var framer inputFramer
	for {
		n, readErr := input.Read(buffer)
		if n > 0 {
			payload, detach, err := framer.Push(buffer[:n])
			if err != nil {
				return err
			}
			if writable && len(payload) != 0 {
				if len(payload) > maxInputBytes {
					return fmt.Errorf("input frame exceeds %d bytes", maxInputBytes)
				}
				written, err := stream.SendInput(ctx, agentID, string(payload))
				if err != nil {
					return err
				}
				if written != len(payload) {
					return fmt.Errorf(
						"input acknowledgement wrote %d/%d bytes",
						written,
						len(payload),
					)
				}
			}
			if detach {
				return errLocalDetach
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if err := framer.End(); err != nil {
					return err
				}
			}
			return readErr
		}
	}
}

func pumpResize(
	ctx context.Context,
	stream terminalStream,
	signals <-chan os.Signal,
	agentID string,
	fd uintptr,
	getSize func(uintptr) (int, int, error),
) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-signals:
			if !ok {
				return nil
			}
			rows, columns, err := getSize(fd)
			if err != nil {
				return fmt.Errorf("read terminal size: %w", err)
			}
			if err := stream.Resize(ctx, agentID, rows, columns); err != nil {
				return err
			}
		}
	}
}

func significantPumpError(err error) error {
	switch {
	case err == nil,
		errors.Is(err, io.EOF),
		errors.Is(err, errLocalDetach),
		errors.Is(err, cancelreader.ErrCanceled),
		errors.Is(err, context.Canceled):
		return nil
	default:
		return err
	}
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) != 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
