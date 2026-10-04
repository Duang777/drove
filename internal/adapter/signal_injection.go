package adapter

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/Duang777/drove/internal/agent"
)

// SignalChannel identifies the authority produced by one injection plan.
type SignalChannel string

const (
	// SignalChannelHook produces authoritative native hook deliveries.
	SignalChannelHook SignalChannel = "hook"
	// SignalChannelNotify produces non-authoritative vendor notifications.
	SignalChannelNotify SignalChannel = "notify"
)

var (
	// ErrUnsupportedSignalInjection indicates that an adapter has no plan.
	ErrUnsupportedSignalInjection = errors.New(
		"adapter: session signal injection is unsupported",
	)
	// ErrSignalInjectionConflict indicates an explicit caller argument conflict.
	ErrSignalInjectionConflict = errors.New(
		"adapter: session signal injection conflicts with caller arguments",
	)
)

// SignalInjectionRequest contains immutable launch facts for one vendor.
type SignalInjectionRequest struct {
	Mode        agent.RunMode
	BaseArgs    []string
	RequestArgs []string
	RelayPath   string
	SessionDir  string
}

// SignalInjectionFile describes one session-private file.
type SignalInjectionFile struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

// SignalInjectionPlan is a complete vendor argument and file plan.
type SignalInjectionPlan struct {
	Args                  []string
	Files                 []SignalInjectionFile
	Channel               SignalChannel
	TerminalNotifications bool
}

// SignalInjector plans process-local vendor signal configuration.
type SignalInjector interface {
	InjectSignals(SignalInjectionRequest) (SignalInjectionPlan, error)
}

func injectSignals(
	injector SignalInjector,
	request SignalInjectionRequest,
) (SignalInjectionPlan, error) {
	if !agent.ValidRunMode(request.Mode) {
		return SignalInjectionPlan{}, fmt.Errorf(
			"adapter: invalid run mode %q",
			request.Mode,
		)
	}
	if !filepath.IsAbs(request.RelayPath) {
		return SignalInjectionPlan{}, errors.New(
			"adapter: signal relay path must be absolute",
		)
	}
	if !filepath.IsAbs(request.SessionDir) {
		return SignalInjectionPlan{}, errors.New(
			"adapter: signal session directory must be absolute",
		)
	}
	request.BaseArgs = append([]string(nil), request.BaseArgs...)
	request.RequestArgs = append([]string(nil), request.RequestArgs...)
	plan, err := injector.InjectSignals(request)
	if err != nil {
		return SignalInjectionPlan{}, err
	}
	if plan.Channel != SignalChannelHook && plan.Channel != SignalChannelNotify {
		return SignalInjectionPlan{}, fmt.Errorf(
			"adapter: invalid signal channel %q",
			plan.Channel,
		)
	}
	seen := make(map[string]struct{}, len(plan.Files))
	for i := range plan.Files {
		file := &plan.Files[i]
		if file.Path == "" ||
			filepath.IsAbs(file.Path) ||
			filepath.Clean(file.Path) != file.Path ||
			file.Path == "." ||
			strings.Contains(file.Path, string(filepath.Separator)) {
			return SignalInjectionPlan{}, fmt.Errorf(
				"adapter: invalid injection file path %q",
				file.Path,
			)
		}
		if _, exists := seen[file.Path]; exists {
			return SignalInjectionPlan{}, fmt.Errorf(
				"adapter: duplicate injection file path %q",
				file.Path,
			)
		}
		seen[file.Path] = struct{}{}
		file.Content = append([]byte(nil), file.Content...)
	}
	plan.Args = append([]string(nil), plan.Args...)
	plan.Files = append([]SignalInjectionFile(nil), plan.Files...)
	return plan, nil
}

func managedRelayArgs(vendor string, payloadArgv bool) []string {
	args := []string{
		"hook",
		"--vendor",
		vendor,
		"--managed-by",
		"drove/v1",
	}
	if payloadArgv {
		args = append(args, "--payload-argv")
	}
	return args
}

func shellCommand(path string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(path))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
