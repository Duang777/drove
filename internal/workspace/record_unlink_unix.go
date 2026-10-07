//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const (
	recordDeletionNamespace = ".drove-delete-v1"

	recordDeletionSource    = "source"
	recordDeletionWitness   = "witness"
	recordDeletionValidated = "validated"
	recordDeletionCandidate = "candidate"
)

func unlinkRecordPath(
	directory *os.File,
	expected *os.File,
	name string,
) error {
	return unlinkRecordPathAfterValidation(
		directory,
		expected,
		name,
		nil,
	)
}

func unlinkRecordPathAfterValidation(
	directory *os.File,
	expected *os.File,
	name string,
	afterValidation func(string),
) (result error) {
	if err := validateRecordDeletionSourceName(name); err != nil {
		return err
	}
	namespace, err := openRecordDeletionNamespace(directory)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, namespace.Close())
	}()
	if err := recoverRecordDeletionTransactions(directory, namespace); err != nil {
		return err
	}

	transactionName := uuid.NewString()
	transaction, err := createRecordDeletionTransaction(
		namespace,
		transactionName,
		name,
	)
	if err != nil {
		return err
	}
	transactionPending := true
	finish := func(operationErr error) error {
		closeErr := transaction.Close()
		transactionPending = false
		settleErr := settleRecordDeletionTransaction(
			directory,
			namespace,
			transactionName,
		)
		return errors.Join(operationErr, closeErr, settleErr)
	}
	defer func() {
		if transactionPending {
			result = errors.Join(result, finish(nil))
		}
	}()

	// The private witness lets recovery distinguish the owned file from a
	// replacement moved into the transaction after validation.
	if err := unix.Linkat(
		int(directory.Fd()),
		name,
		int(transaction.Fd()),
		recordDeletionWitness,
		0,
	); err != nil {
		return finish(fmt.Errorf(
			"create record deletion witness for %q: %w",
			name,
			err,
		))
	}
	if err := transaction.Sync(); err != nil {
		return finish(fmt.Errorf(
			"sync record deletion witness for %q: %w",
			name,
			err,
		))
	}
	if err := verifyRecordPathIdentity(
		transaction,
		expected,
		recordDeletionWitness,
	); err != nil {
		return finish(fmt.Errorf(
			"verify record deletion witness for %q: %w",
			name,
			err,
		))
	}
	if err := markRecordDeletionValidated(transaction); err != nil {
		return finish(err)
	}
	if afterValidation != nil {
		afterValidation(name)
	}
	if err := unix.Renameat(
		int(directory.Fd()),
		name,
		int(transaction.Fd()),
		recordDeletionCandidate,
	); err != nil {
		return finish(fmt.Errorf(
			"move record deletion candidate %q: %w",
			name,
			err,
		))
	}
	verifyErr := verifyRecordPathIdentity(
		transaction,
		expected,
		recordDeletionCandidate,
	)
	if verifyErr != nil {
		verifyErr = fmt.Errorf(
			"record deletion candidate %q changed identity: %w",
			name,
			verifyErr,
		)
	}
	return finish(verifyErr)
}

func openRecordDeletionNamespace(
	directory *os.File,
) (*os.File, error) {
	parentFD := int(directory.Fd())
	created := false
	if err := unix.Mkdirat(
		parentFD,
		recordDeletionNamespace,
		0o700,
	); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, fmt.Errorf(
			"create private record deletion namespace: %w",
			err,
		)
	}
	namespace, err := openRecordDeletionDirectoryAt(
		directory,
		recordDeletionNamespace,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"open private record deletion namespace: %w",
			err,
		)
	}
	if created {
		if err := unix.Fchmod(int(namespace.Fd()), 0o700); err != nil {
			return nil, errors.Join(
				fmt.Errorf(
					"protect private record deletion namespace: %w",
					err,
				),
				namespace.Close(),
			)
		}
	}
	if err := validateRecordDeletionDirectory(namespace); err != nil {
		return nil, errors.Join(err, namespace.Close())
	}
	if err := unix.Flock(int(namespace.Fd()), unix.LOCK_EX); err != nil {
		return nil, errors.Join(
			fmt.Errorf("lock private record deletion namespace: %w", err),
			namespace.Close(),
		)
	}
	if created {
		if err := errors.Join(namespace.Sync(), directory.Sync()); err != nil {
			return nil, errors.Join(
				fmt.Errorf(
					"sync private record deletion namespace: %w",
					err,
				),
				namespace.Close(),
			)
		}
	}
	return namespace, nil
}

func openRecordDeletionDirectoryAt(
	parent *os.File,
	name string,
) (*os.File, error) {
	fd, err := unix.Openat(
		int(parent.Fd()),
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY,
		0,
	)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), name)
	if directory == nil {
		_ = unix.Close(fd)
		return nil, errors.New("create record deletion directory handle")
	}
	return directory, nil
}

func validateRecordDeletionDirectory(directory *os.File) error {
	var info unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &info); err != nil {
		return fmt.Errorf("inspect private record deletion directory: %w", err)
	}
	mode := uint32(info.Mode)
	if mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New(
			"workspace: private record deletion path is not a directory",
		)
	}
	if info.Uid != uint32(os.Geteuid()) {
		return errors.New(
			"workspace: private record deletion directory has the wrong owner",
		)
	}
	if mode&0o777 != 0o700 {
		return errors.New(
			"workspace: private record deletion directory has unsafe permissions",
		)
	}
	return nil
}

func createRecordDeletionTransaction(
	namespace *os.File,
	name string,
	sourceName string,
) (*os.File, error) {
	if err := unix.Mkdirat(int(namespace.Fd()), name, 0o700); err != nil {
		return nil, fmt.Errorf("create record deletion transaction: %w", err)
	}
	transaction, err := openRecordDeletionDirectoryAt(namespace, name)
	if err != nil {
		removeErr := unix.Unlinkat(
			int(namespace.Fd()),
			name,
			unix.AT_REMOVEDIR,
		)
		return nil, errors.Join(
			fmt.Errorf("open record deletion transaction: %w", err),
			removeErr,
			namespace.Sync(),
		)
	}
	discard := func(cause error) (*os.File, error) {
		removeSourceErr := unix.Unlinkat(
			int(transaction.Fd()),
			recordDeletionSource,
			0,
		)
		if errors.Is(removeSourceErr, unix.ENOENT) {
			removeSourceErr = nil
		}
		closeErr := transaction.Close()
		removeTransactionErr := unix.Unlinkat(
			int(namespace.Fd()),
			name,
			unix.AT_REMOVEDIR,
		)
		return nil, errors.Join(
			cause,
			removeSourceErr,
			closeErr,
			removeTransactionErr,
			namespace.Sync(),
		)
	}
	if err := unix.Fchmod(int(transaction.Fd()), 0o700); err != nil {
		return discard(fmt.Errorf(
			"protect record deletion transaction: %w",
			err,
		))
	}
	if err := writeRecordDeletionFile(
		transaction,
		recordDeletionSource,
		[]byte(sourceName),
	); err != nil {
		return discard(fmt.Errorf(
			"write record deletion source: %w",
			err,
		))
	}
	if err := errors.Join(transaction.Sync(), namespace.Sync()); err != nil {
		return discard(fmt.Errorf(
			"sync record deletion transaction: %w",
			err,
		))
	}
	return transaction, nil
}

func markRecordDeletionValidated(transaction *os.File) error {
	if err := writeRecordDeletionFile(
		transaction,
		recordDeletionValidated,
		nil,
	); err != nil {
		return fmt.Errorf("mark record deletion witness validated: %w", err)
	}
	if err := transaction.Sync(); err != nil {
		return fmt.Errorf("sync validated record deletion witness: %w", err)
	}
	return nil
}

func writeRecordDeletionFile(
	directory *os.File,
	name string,
	content []byte,
) error {
	fd, err := unix.Openat(
		int(directory.Fd()),
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("create record deletion metadata handle")
	}
	written, writeErr := file.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func validateRecordDeletionSourceName(name string) error {
	if name == "" ||
		name == "." ||
		name == ".." ||
		name == recordDeletionNamespace ||
		strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf(
			"workspace: record deletion source name %q is invalid",
			name,
		)
	}
	return nil
}

func preserveRecordDeletionPath(
	directory *os.File,
	name string,
	transaction *os.File,
	privateName string,
	privateFile *os.File,
) error {
	err := unix.Linkat(
		int(transaction.Fd()),
		privateName,
		int(directory.Fd()),
		name,
		0,
	)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf(
			"restore record deletion path %q: %w",
			name,
			err,
		)
	}
	current, openErr := openRecordPath(directory, name)
	if openErr != nil {
		return fmt.Errorf(
			"open restored record deletion path %q: %w",
			name,
			openErr,
		)
	}
	privateInfo, statErr := privateFile.Stat()
	currentInfo, currentErr := current.Stat()
	closeErr := current.Close()
	if err := errors.Join(statErr, currentErr, closeErr); err != nil {
		return err
	}
	if !os.SameFile(privateInfo, currentInfo) {
		return fmt.Errorf(
			"workspace: record deletion path %q is occupied by another file",
			name,
		)
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf(
			"sync restored record deletion path %q: %w",
			name,
			err,
		)
	}
	return nil
}

func unlinkPrivateRecordPath(directory *os.File, name string) error {
	if err := unix.Unlinkat(int(directory.Fd()), name, 0); err != nil {
		return fmt.Errorf(
			"remove private record deletion path %q: %w",
			name,
			err,
		)
	}
	return nil
}

func readRecordDeletionDirectory(
	directory *os.File,
) ([]os.DirEntry, error) {
	duplicate, err := openRecordDeletionDirectoryAt(directory, ".")
	if err != nil {
		return nil, err
	}
	entries, readErr := duplicate.ReadDir(-1)
	closeErr := duplicate.Close()
	return entries, errors.Join(readErr, closeErr)
}
