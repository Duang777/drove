package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

type codexSignalInjector struct{}

func (codexSignalInjector) InjectSignals(
	request SignalInjectionRequest,
) (SignalInjectionPlan, error) {
	if codexNotifyConflict(request.RequestArgs) {
		return SignalInjectionPlan{}, fmt.Errorf(
			"%w: notify is already set",
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
	args := make([]string, 0, len(request.BaseArgs)+len(request.RequestArgs)+2)
	args = append(args, "-c", "notify="+notify)
	args = append(args, request.BaseArgs...)
	args = append(args, request.RequestArgs...)
	return SignalInjectionPlan{
		Args:    args,
		Channel: SignalChannelNotify,
	}, nil
}

func codexNotifyConflict(args []string) bool {
	for index, arg := range args {
		switch {
		case arg == "-c" || arg == "--config":
			if index+1 < len(args) && codexConfigKey(args[index+1]) == "notify" {
				return true
			}
		case strings.HasPrefix(arg, "-c="):
			if codexConfigKey(strings.TrimPrefix(arg, "-c=")) == "notify" {
				return true
			}
		case strings.HasPrefix(arg, "--config="):
			if codexConfigKey(strings.TrimPrefix(arg, "--config=")) == "notify" {
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
