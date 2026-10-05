package clitui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Duang777/drove/internal/cliattach"
)

func TestRunRejectsNilClient(t *testing.T) {
	t.Parallel()

	if err := Run(context.Background(), nil); err == nil {
		t.Fatal("Run accepted a nil client")
	}
}

func TestRunUsesAlternateScreenAndClosesPreview(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("close failed")
	preview := newRunTestPreview(closeErr)
	var output bytes.Buffer
	var previewContext context.Context
	err := run(context.Background(), &fakeTUIClient{}, runDependencies{
		newPreview: func(ctx context.Context) previewController {
			previewContext = ctx
			return preview
		},
		attach: func(context.Context, string, cliattach.Options) error {
			return nil
		},
		newProgram: func(
			model tea.Model,
			options ...tea.ProgramOption,
		) tuiProgram {
			options = append(options,
				tea.WithInput(strings.NewReader("q")),
				tea.WithOutput(&output),
				tea.WithoutSignals(),
			)
			return tea.NewProgram(model, options...)
		},
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("run error = %v, want preview close error", err)
	}
	if got := preview.closeCalls.Load(); got != 1 {
		t.Fatalf("preview close calls = %d, want 1", got)
	}
	if err := previewContext.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("runtime context error = %v, want context canceled", err)
	}
	rendered := output.String()
	if !strings.Contains(rendered, "\x1b[?1049h") ||
		!strings.Contains(rendered, "\x1b[?1049l") {
		t.Fatalf("alternate screen lifecycle missing from %q", rendered)
	}
}

func TestRunReturnsParentContextErrorAndClosesPreview(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	preview := newRunTestPreview(nil)
	var previewContext context.Context
	err := run(ctx, &fakeTUIClient{}, runDependencies{
		newPreview: func(got context.Context) previewController {
			previewContext = got
			return preview
		},
		attach: func(context.Context, string, cliattach.Options) error {
			return nil
		},
		newProgram: func(
			model tea.Model,
			options ...tea.ProgramOption,
		) tuiProgram {
			options = append(options,
				tea.WithInput(nil),
				tea.WithOutput(io.Discard),
				tea.WithoutSignals(),
			)
			return tea.NewProgram(model, options...)
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want context canceled", err)
	}
	if got := preview.closeCalls.Load(); got != 1 {
		t.Fatalf("preview close calls = %d, want 1", got)
	}
	if err := previewContext.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("preview context error = %v, want context canceled", err)
	}
}

type runTestPreview struct {
	events     chan previewEvent
	closeErr   error
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func newRunTestPreview(closeErr error) *runTestPreview {
	return &runTestPreview{
		events:   make(chan previewEvent),
		closeErr: closeErr,
	}
}

func (*runTestPreview) Replace(previewTarget) {}

func (p *runTestPreview) Events() <-chan previewEvent {
	return p.events
}

func (p *runTestPreview) Close() error {
	p.closeCalls.Add(1)
	p.closeOnce.Do(func() {
		close(p.events)
	})
	return p.closeErr
}
