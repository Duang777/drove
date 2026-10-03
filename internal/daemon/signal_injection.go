package daemon

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Duang777/drove/internal/agent"
	"github.com/Duang777/drove/internal/config"
	"github.com/Duang777/drove/internal/session"
)

func signalInjectionOption(cfg *config.Config) session.ManagerOption {
	modes := make(map[string]agent.SignalInjectionMode, len(cfg.Agents))
	for vendor, settings := range cfg.Agents {
		if settings.SignalInjection != "" {
			modes[vendor] = agent.SignalInjectionMode(settings.SignalInjection)
		}
	}
	return session.WithSignalInjection(session.SignalInjectionOptions{
		DataDir:   cfg.DataDir,
		RelayPath: resolveDroveCLI(),
		Modes:     modes,
	})
}

func resolveDroveCLI() string {
	if executable, err := os.Executable(); err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			if candidate := executableFile(filepath.Join(filepath.Dir(resolved), "drove")); candidate != "" {
				return candidate
			}
		}
	}
	if candidate, err := exec.LookPath("drove"); err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
			return executableFile(resolved)
		}
	}
	return ""
}

func executableFile(path string) string {
	info, err := os.Lstat(path)
	if err != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm()&0o111 == 0 {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	return absolute
}
