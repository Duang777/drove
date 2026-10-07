//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
)

const (
	recordInstallAliasPrefix = ".drove-install-"
	recordRemoveLegacyPrefix = ".drove-remove-"
)

type recordInstallAlias struct {
	name string
}

func recoverRecordRenameDebris(root *os.Root) (result error) {
	if err := cleanupRecordDeletionNamespace(root); err != nil {
		return fmt.Errorf(
			"workspace: recover record deletion transactions: %w",
			err,
		)
	}
	entries, err := readRootDirectory(root)
	if err != nil {
		return err
	}
	aliases := make([]recordInstallAlias, 0)
	for _, entry := range entries {
		switch {
		case strings.HasPrefix(entry.Name(), recordRemoveLegacyPrefix):
			return fmt.Errorf(
				"workspace: legacy record removal artifact %q has no recoverable owner",
				entry.Name(),
			)
		case strings.HasPrefix(entry.Name(), recordInstallAliasPrefix):
			if !validRecordInstallAliasName(entry.Name()) {
				return fmt.Errorf(
					"workspace: record install artifact %q has an invalid name",
					entry.Name(),
				)
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf(
					"workspace: record install artifact %q is not a regular file",
					entry.Name(),
				)
			}
			aliases = append(aliases, recordInstallAlias{name: entry.Name()})
		}
	}
	if len(aliases) == 0 {
		return nil
	}
	directory, err := openRecordDirectory(root)
	if err != nil {
		return err
	}
	directoryOpen := true
	defer func() {
		if directoryOpen {
			result = errors.Join(result, directory.Close())
		}
	}()
	for _, alias := range aliases {
		file, err := openRecordPath(directory, alias.name)
		if err != nil {
			return fmt.Errorf(
				"workspace: open record install artifact %q: %w",
				alias.name,
				err,
			)
		}
		witness, witnessErr := findRecordInstallAliasWitness(
			directory,
			entries,
			alias.name,
			file,
		)
		if witnessErr == nil {
			witnessErr = unlinkLinkedRecordPath(
				directory,
				file,
				alias.name,
				witness,
			)
		}
		closeErr := file.Close()
		if err := errors.Join(witnessErr, closeErr); err != nil {
			return err
		}
	}
	if err := syncRecordDirectory(directory); err != nil {
		return err
	}
	directoryOpen = false
	if err := directory.Close(); err != nil {
		return err
	}
	if err := cleanupRecordDeletionNamespace(root); err != nil {
		return fmt.Errorf(
			"workspace: cleanup record deletion transactions: %w",
			err,
		)
	}
	return nil
}

func validRecordInstallAliasName(name string) bool {
	raw, found := strings.CutPrefix(name, recordInstallAliasPrefix)
	if !found || len(raw) != 36 {
		return false
	}
	parsed, err := uuid.Parse(raw)
	return err == nil && parsed.String() == raw
}

func findRecordInstallAliasWitness(
	directory *os.File,
	entries []os.DirEntry,
	aliasName string,
	alias *os.File,
) (string, error) {
	aliasInfo, err := alias.Stat()
	if err != nil {
		return "", err
	}
	if !aliasInfo.Mode().IsRegular() {
		return "", fmt.Errorf(
			"workspace: record install artifact %q is not a regular file",
			aliasName,
		)
	}
	for _, entry := range entries {
		if entry.Name() == aliasName ||
			strings.HasPrefix(entry.Name(), recordInstallAliasPrefix) ||
			!entry.Type().IsRegular() {
			continue
		}
		candidate, err := openRecordPath(directory, entry.Name())
		if err != nil {
			return "", err
		}
		candidateInfo, statErr := candidate.Stat()
		closeErr := candidate.Close()
		if err := errors.Join(statErr, closeErr); err != nil {
			return "", err
		}
		if candidateInfo.Mode().IsRegular() &&
			os.SameFile(aliasInfo, candidateInfo) {
			return entry.Name(), nil
		}
	}
	return "", fmt.Errorf(
		"workspace: record install artifact %q has no same-file owner",
		aliasName,
	)
}
