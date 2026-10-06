package clitui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Duang777/drove/internal/cliattach"
	"github.com/Duang777/drove/internal/client"
)

type tuiProgram interface {
	Run() (tea.Model, error)
}

type runDependencies struct {
	newPreview func(context.Context) previewController
	attach     attachRunner
	newProgram func(tea.Model, ...tea.ProgramOption) tuiProgram
}

// Run opens the fleet overview until the user quits or ctx ends.
func Run(ctx context.Context, daemon *client.Client) error {
	if daemon == nil {
		return errors.New("clitui: client is required")
	}
	return run(ctx, daemon, runDependencies{
		newPreview: func(ctx context.Context) previewController {
			return newPreviewActor(
				ctx,
				func(ctx context.Context) (previewStream, error) {
					return daemon.OpenTerminal(ctx)
				},
			)
		},
		attach: func(
			ctx context.Context,
			agentID string,
			options cliattach.Options,
		) error {
			return cliattach.Run(ctx, daemon, agentID, options)
		},
		newProgram: func(
			model tea.Model,
			options ...tea.ProgramOption,
		) tuiProgram {
			return tea.NewProgram(model, options...)
		},
	})
}

func run(
	ctx context.Context,
	daemon tuiClient,
	deps runDependencies,
) (retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if daemon == nil {
		return errors.New("clitui: client is required")
	}
	if deps.newPreview == nil || deps.attach == nil || deps.newProgram == nil {
		return errors.New("clitui: runtime dependencies are incomplete")
	}

	runCtx, cancel := context.WithCancel(ctx)
	preview := deps.newPreview(runCtx)
	if preview == nil {
		cancel()
		return errors.New("clitui: preview controller is required")
	}
	defer func() {
		cancel()
		if err := preview.Close(); err != nil {
			retErr = errors.Join(
				retErr,
				fmt.Errorf("clitui: close terminal preview: %w", err),
			)
		}
	}()

	program := deps.newProgram(
		newModel(runCtx, daemon, preview, deps.attach),
		tea.WithAltScreen(),
		tea.WithContext(runCtx),
	)
	if program == nil {
		return errors.New("clitui: Bubble Tea program is required")
	}
	if _, err := program.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("clitui: run Bubble Tea program: %w", err)
	}
	return nil
}
