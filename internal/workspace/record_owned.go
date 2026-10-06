package workspace

import (
	"errors"
	"fmt"
	"os"
)

func removeOwnedRecordPathIfSame(
	root *os.Root,
	name string,
	expected *os.File,
	isolatedName string,
	validate func(*os.File) error,
) error {
	_, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return removeOwnedRecordPath(
		root,
		name,
		expected,
		isolatedName,
		validate,
		nil,
	)
}

func removeOwnedRecordPath(
	root *os.Root,
	name string,
	expected *os.File,
	isolatedName string,
	validate func(*os.File) error,
	afterValidation func(),
) (result error) {
	if name == isolatedName {
		return errors.New(
			"workspace: owned record source and isolation names match",
		)
	}
	if validate != nil {
		if err := validate(expected); err != nil {
			return err
		}
	}
	if afterValidation != nil {
		afterValidation()
	}
	directory, err := openRecordDirectory(root)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, directory.Close())
	}()
	moved, err := moveRecordFile(
		directory,
		expected,
		name,
		isolatedName,
	)
	if err != nil {
		return fmt.Errorf(
			"workspace: isolate owned record path %q: %w",
			name,
			err,
		)
	}
	if !moved {
		return fmt.Errorf(
			"workspace: owned record path %q was not isolated",
			name,
		)
	}
	if err := syncRecordDirectory(directory); err != nil {
		return fmt.Errorf(
			"workspace: sync isolated owned record path %q: %w",
			name,
			err,
		)
	}
	if validate != nil {
		if err := validate(expected); err != nil {
			return err
		}
	}
	if err := unlinkOwnedRecordPath(
		directory,
		expected,
		isolatedName,
	); err != nil {
		return err
	}
	if err := syncRecordDirectory(directory); err != nil {
		return fmt.Errorf(
			"workspace: sync removed owned record path %q: %w",
			name,
			err,
		)
	}
	return nil
}
