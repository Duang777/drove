package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

type codexSignalInjector struct{}

var codexManagedConfigKeys = map[string]struct{}{
	"notify":                     {},
	"tui.notifications":          {},
	"tui.notification_method":    {},
	"tui.notification_condition": {},
}

func (codexSignalInjector) InjectSignals(
	request SignalInjectionRequest,
) (SignalInjectionPlan, error) {
	if codexSignalInjectionConflict(request.BaseArgs) ||
		codexSignalInjectionConflict(request.RequestArgs) {
		return SignalInjectionPlan{}, fmt.Errorf(
			"%w: a managed Codex notification key is already set",
			ErrSignalInjectionConflict,
		)
	}

	command := append(
		[]string{request.RelayPath},
		managedRelayArgs("codex", true)...,
	)
	notify, err := encodeTOMLStringArray(command)
	if err != nil {
		return SignalInjectionPlan{}, err
	}
	args := make([]string, 0, len(request.BaseArgs)+len(request.RequestArgs)+8)
	args = append(args, "-c", "notify="+notify)
	args = append(args, "-c", `tui.notifications=["approval-requested"]`)
	args = append(args, "-c", `tui.notification_method="osc9"`)
	args = append(args, "-c", `tui.notification_condition="always"`)
	args = append(args, request.BaseArgs...)
	args = append(args, request.RequestArgs...)
	return SignalInjectionPlan{
		Args:                  args,
		Channel:               SignalChannelNotify,
		TerminalNotifications: true,
	}, nil
}

func codexSignalInjectionConflict(args []string) bool {
	for index, arg := range args {
		switch {
		case arg == "-c" || arg == "--config":
			if index+1 < len(args) {
				if _, managed := codexManagedConfigKeys[codexConfigKey(args[index+1])]; managed {
					return true
				}
			}
		case strings.HasPrefix(arg, "-c="):
			if _, managed := codexManagedConfigKeys[codexConfigKey(strings.TrimPrefix(arg, "-c="))]; managed {
				return true
			}
		case strings.HasPrefix(arg, "--config="):
			if _, managed := codexManagedConfigKeys[codexConfigKey(strings.TrimPrefix(arg, "--config="))]; managed {
				return true
			}
		}
	}
	return false
}

func codexConfigKey(value string) string {
	key, _, found := strings.Cut(value, "=")
	if !found {
		return ""
	}
	return strings.TrimSpace(key)
}

func encodeTOMLStringArray(values []string) (string, error) {
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		item, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("adapter: encode Codex notify argument: %w", err)
		}
		encoded = append(encoded, string(item))
	}
	return "[" + strings.Join(encoded, ",") + "]", nil
}
