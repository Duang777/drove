//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func recoverRecordDeletionTransactions(
	directory *os.File,
	namespace *os.File,
) error {
	entries, err := readRecordDeletionDirectory(namespace)
	if err != nil {
		return fmt.Errorf("read private record deletion namespace: %w", err)
	}
	for _, entry := range entries {
		if !canonicalUUID(entry.Name()) {
			return fmt.Errorf(
				"workspace: private record deletion artifact %q is invalid",
				entry.Name(),
			)
		}
		if err := settleRecordDeletionTransaction(
			directory,
			namespace,
			entry.Name(),
		); err != nil {
			return fmt.Errorf(
				"workspace: recover record deletion transaction %q: %w",
				entry.Name(),
				err,
			)
		}
	}
	return nil
}

func settleRecordDeletionTransaction(
	directory *os.File,
	namespace *os.File,
	name string,
) (result error) {
	transaction, err := openRecordDeletionDirectoryAt(namespace, name)
	if err != nil {
		return err
	}
	transactionOpen := true
	defer func() {
		if transactionOpen {
			result = errors.Join(result, transaction.Close())
		}
	}()
	if err := validateRecordDeletionDirectory(transaction); err != nil {
		return err
	}
	entries, err := readRecordDeletionDirectory(transaction)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if err := transaction.Close(); err != nil {
			return err
		}
		transactionOpen = false
		return removeSettledRecordDeletionTransaction(
			namespace,
			name,
		)
	}
	sourceName, validated, witness, candidate, err :=
		inspectRecordDeletionTransaction(transaction)
	if err != nil {
		return err
	}
	closeFiles := func() error {
		var closeErr error
		if candidate != nil {
			closeErr = errors.Join(closeErr, candidate.Close())
			candidate = nil
		}
		if witness != nil {
			closeErr = errors.Join(closeErr, witness.Close())
			witness = nil
		}
		return closeErr
	}
	defer func() {
		result = errors.Join(result, closeFiles())
	}()

	switch {
	case witness == nil && candidate != nil:
		return errors.New(
			"workspace: record deletion candidate has no witness",
		)
	case !validated && candidate != nil:
		return errors.New(
			"workspace: unvalidated record deletion has a candidate",
		)
	case witness != nil && !validated:
		if err := preserveRecordDeletionPath(
			directory,
			sourceName,
			transaction,
			recordDeletionWitness,
			witness,
		); err != nil {
			return err
		}
		if err := unlinkPrivateRecordPath(
			transaction,
			recordDeletionWitness,
		); err != nil {
			return err
		}
	case witness != nil && candidate != nil:
		witnessInfo, statErr := witness.Stat()
		candidateInfo, candidateErr := candidate.Stat()
		if err := errors.Join(statErr, candidateErr); err != nil {
			return err
		}
		if !os.SameFile(witnessInfo, candidateInfo) {
			if err := preserveRecordDeletionPath(
				directory,
				sourceName,
				transaction,
				recordDeletionCandidate,
				candidate,
			); err != nil {
				return err
			}
		}
		if err := unlinkPrivateRecordPath(
			transaction,
			recordDeletionCandidate,
		); err != nil {
			return err
		}
		if err := unlinkPrivateRecordPath(
			transaction,
			recordDeletionWitness,
		); err != nil {
			return err
		}
	case witness != nil:
		if err := unlinkPrivateRecordPath(
			transaction,
			recordDeletionWitness,
		); err != nil {
			return err
		}
	}
	if err := closeFiles(); err != nil {
		return err
	}
	if validated {
		if err := unlinkPrivateRecordPath(
			transaction,
			recordDeletionValidated,
		); err != nil {
			return err
		}
	}
	if err := unlinkPrivateRecordPath(
		transaction,
		recordDeletionSource,
	); err != nil {
		return err
	}
	if err := transaction.Close(); err != nil {
		return err
	}
	transactionOpen = false
	return removeSettledRecordDeletionTransaction(namespace, name)
}

func removeSettledRecordDeletionTransaction(
	namespace *os.File,
	name string,
) error {
	if err := unix.Unlinkat(
		int(namespace.Fd()),
		name,
		unix.AT_REMOVEDIR,
	); err != nil {
		return fmt.Errorf("remove settled record deletion transaction: %w", err)
	}
	if err := namespace.Sync(); err != nil {
		return fmt.Errorf("sync settled record deletion namespace: %w", err)
	}
	return nil
}

func inspectRecordDeletionTransaction(
	transaction *os.File,
) (
	sourceName string,
	validated bool,
	witness *os.File,
	candidate *os.File,
	result error,
) {
	entries, err := readRecordDeletionDirectory(transaction)
	if err != nil {
		return "", false, nil, nil, err
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		switch entry.Name() {
		case recordDeletionSource,
			recordDeletionValidated,
			recordDeletionWitness,
			recordDeletionCandidate:
		default:
			return "", false, nil, nil, fmt.Errorf(
				"workspace: record deletion transaction entry %q is invalid",
				entry.Name(),
			)
		}
		if seen[entry.Name()] || !entry.Type().IsRegular() {
			return "", false, nil, nil, fmt.Errorf(
				"workspace: record deletion transaction entry %q is invalid",
				entry.Name(),
			)
		}
		seen[entry.Name()] = true
	}
	if !seen[recordDeletionSource] {
		return "", false, nil, nil, errors.New(
			"workspace: record deletion transaction has no source",
		)
	}
	sourceName, err = readRecordDeletionSource(transaction)
	if err != nil {
		return "", false, nil, nil, err
	}
	validated = seen[recordDeletionValidated]
	if seen[recordDeletionWitness] {
		witness, err = openRecordPath(
			transaction,
			recordDeletionWitness,
		)
		if err != nil {
			return "", false, nil, nil, err
		}
	}
	if seen[recordDeletionCandidate] {
		candidate, err = openRecordPath(
			transaction,
			recordDeletionCandidate,
		)
		if err != nil {
			if witness != nil {
				err = errors.Join(err, witness.Close())
			}
			return "", false, nil, nil, err
		}
	}
	return sourceName, validated, witness, candidate, nil
}

func readRecordDeletionSource(transaction *os.File) (string, error) {
	file, err := openRecordPath(transaction, recordDeletionSource)
	if err != nil {
		return "", err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return "", err
	}
	if len(raw) > 4096 {
		return "", errors.New(
			"workspace: record deletion source name is too long",
		)
	}
	name := string(raw)
	if err := validateRecordDeletionSourceName(name); err != nil {
		return "", err
	}
	return name, nil
}
