//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"errors"
	"fmt"
	"os"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

func renameRecordFile(
	directory *os.File,
	temporary *os.File,
	temporaryName string,
	recordName string,
	replace bool,
) (bool, error) {
	return renameRecordPath(
		directory,
		temporary,
		temporaryName,
		recordName,
		replace,
	)
}

func moveRecordFile(
	directory *os.File,
	source *os.File,
	sourceName string,
	targetName string,
) (bool, error) {
	return renameRecordPath(
		directory,
		source,
		sourceName,
		targetName,
		false,
	)
}

func renameRecordPath(
	directory *os.File,
	expected *os.File,
	sourceName string,
	targetName string,
	replace bool,
) (bool, error) {
	return renameRecordPathAfterValidation(
		directory,
		expected,
		sourceName,
		targetName,
		replace,
		nil,
	)
}

func renameRecordPathAfterValidation(
	directory *os.File,
	expected *os.File,
	sourceName string,
	targetName string,
	replace bool,
	afterValidation func(),
) (installed bool, result error) {
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		sourceName,
	); err != nil {
		return false, err
	}
	if afterValidation != nil {
		afterValidation()
	}
	fd := int(directory.Fd())
	alias := ".drove-install-" + uuid.NewString()
	if err := unix.Linkat(fd, sourceName, fd, alias, 0); err != nil {
		return false, err
	}
	aliasFile, err := openRecordPath(directory, alias)
	if err != nil {
		return false, fmt.Errorf("open staged record alias: %w", err)
	}
	aliasPresent := true
	defer func() {
		if aliasPresent {
			result = errors.Join(
				result,
				unlinkLinkedRecordPath(
					directory,
					aliasFile,
					alias,
					sourceName,
				),
			)
		}
		result = errors.Join(result, aliasFile.Close())
	}()
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		alias,
	); err != nil {
		return false, fmt.Errorf(
			"verify staged record identity: %w",
			err,
		)
	}
	if replace {
		if err := unix.Renameat(fd, alias, fd, targetName); err != nil {
			return false, err
		}
		aliasPresent = false
		if err := verifyRecordPathIdentity(
			directory,
			expected,
			targetName,
		); err != nil {
			return true, fmt.Errorf(
				"verify renamed record identity: %w",
				err,
			)
		}
		if err := unlinkLinkedRecordPath(
			directory,
			expected,
			sourceName,
			targetName,
		); err != nil {
			return true, fmt.Errorf(
				"remove installed record source: %w",
				err,
			)
		}
		return true, nil
	}
	if err := unix.Linkat(fd, alias, fd, targetName, 0); err != nil {
		return false, err
	}
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		targetName,
	); err != nil {
		return true, fmt.Errorf(
			"verify linked record identity: %w",
			err,
		)
	}
	if err := unlinkLinkedRecordPath(
		directory,
		aliasFile,
		alias,
		targetName,
	); err != nil {
		return true, err
	}
	aliasPresent = false
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		targetName,
	); err != nil {
		return true, fmt.Errorf(
			"reverify installed record identity: %w",
			err,
		)
	}
	if err := unlinkLinkedRecordPath(
		directory,
		expected,
		sourceName,
		targetName,
	); err != nil {
		return true, fmt.Errorf(
			"remove installed record source: %w",
			err,
		)
	}
	return true, nil
}

func unlinkLinkedRecordPath(
	directory *os.File,
	expected *os.File,
	name string,
	witnessName string,
) error {
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		name,
	); err != nil {
		return err
	}
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		witnessName,
	); err != nil {
		return fmt.Errorf("verify linked record witness: %w", err)
	}
	fd := int(directory.Fd())
	isolatedName := ".drove-install-" + uuid.NewString()
	if err := unix.Renameat(fd, name, fd, isolatedName); err != nil {
		return fmt.Errorf("isolate linked record path %q: %w", name, err)
	}
	restore := func() error {
		if err := unix.Linkat(fd, isolatedName, fd, name, 0); err != nil {
			return fmt.Errorf(
				"restore linked record path %q: %w",
				name,
				err,
			)
		}
		if err := unix.Unlinkat(fd, isolatedName, 0); err != nil {
			return fmt.Errorf(
				"remove restored linked record isolation %q: %w",
				isolatedName,
				err,
			)
		}
		return nil
	}
	if err := errors.Join(
		verifyRecordPathIdentity(directory, expected, isolatedName),
		verifyRecordPathIdentity(directory, expected, witnessName),
	); err != nil {
		return errors.Join(
			fmt.Errorf("verify isolated linked record path %q: %w", name, err),
			restore(),
		)
	}
	if err := unix.Unlinkat(fd, isolatedName, 0); err != nil {
		return errors.Join(
			fmt.Errorf("remove isolated linked record path %q: %w", name, err),
			restore(),
		)
	}
	return nil
}

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
	afterValidation func(),
) error {
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		name,
	); err != nil {
		return err
	}
	if afterValidation != nil {
		afterValidation()
	}
	fd := int(directory.Fd())
	isolatedName := ".drove-remove-" + uuid.NewString()
	if err := unix.Renameat(fd, name, fd, isolatedName); err != nil {
		return fmt.Errorf("isolate record path %q: %w", name, err)
	}
	restore := func() error {
		if err := unix.Linkat(
			fd,
			isolatedName,
			fd,
			name,
			0,
		); err != nil {
			return fmt.Errorf(
				"restore isolated record path %q: %w",
				name,
				err,
			)
		}
		if err := unix.Unlinkat(fd, isolatedName, 0); err != nil {
			return fmt.Errorf(
				"remove restored record isolation %q: %w",
				isolatedName,
				err,
			)
		}
		return nil
	}
	if err := verifyRecordPathIdentity(
		directory,
		expected,
		isolatedName,
	); err != nil {
		return errors.Join(
			fmt.Errorf("verify isolated record path %q: %w", name, err),
			restore(),
		)
	}
	if err := unix.Unlinkat(fd, isolatedName, 0); err != nil {
		return errors.Join(
			fmt.Errorf("remove isolated record path %q: %w", name, err),
			restore(),
		)
	}
	return nil
}

func verifyRecordPathIdentity(
	directory *os.File,
	expected *os.File,
	name string,
) (result error) {
	expectedInfo, err := expected.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened record: %w", err)
	}
	current, err := openRecordPath(directory, name)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, current.Close())
	}()
	currentInfo, err := current.Stat()
	if err != nil {
		return fmt.Errorf("inspect record path %q: %w", name, err)
	}
	if !os.SameFile(expectedInfo, currentInfo) {
		return fmt.Errorf("record path %q changed identity", name)
	}
	return nil
}

func openRecordPath(
	directory *os.File,
	name string,
) (*os.File, error) {
	fd, err := unix.Openat(
		int(directory.Fd()),
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open record path %q: %w", name, err)
	}
	current := os.NewFile(uintptr(fd), name)
	if current == nil {
		_ = unix.Close(fd)
		return nil, errors.New("create record file from descriptor")
	}
	currentInfo, err := current.Stat()
	if err != nil {
		_ = current.Close()
		return nil, fmt.Errorf("inspect record path %q: %w", name, err)
	}
	if !currentInfo.Mode().IsRegular() ||
		currentInfo.Mode()&os.ModeSymlink != 0 {
		_ = current.Close()
		return nil, fmt.Errorf("record path %q is not a regular file", name)
	}
	return current, nil
}

func openRecordDirectory(root *os.Root) (*os.File, error) {
	return root.Open(".")
}

func syncRecordDirectory(directory *os.File) error {
	return directory.Sync()
}
