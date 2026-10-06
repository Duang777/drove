//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	preparedCheckoutRegularMode    = "100644"
	preparedCheckoutExecutableMode = "100755"
	preparedCheckoutSymlinkMode    = "120000"
	preparedCheckoutGitlinkMode    = "160000"
)

type preparedCheckoutEntry struct {
	mode string
	oid  string
	path string
	temp string
}

func checkoutPreparedWorktree(
	ctx context.Context,
	repository repositoryCapability,
	_ string,
	worktreeRoot *os.Root,
	expectedHeadOID string,
) (result error) {
	if _, err := repository.runPrivateGitAt(
		ctx,
		".",
		"",
		"read-tree",
		"--reset",
		expectedHeadOID,
	); err != nil {
		return err
	}
	index, err := repository.runPrivateGitAt(
		ctx,
		".",
		"",
		"ls-files",
		"--stage",
		"-z",
		"--",
	)
	if err != nil {
		return err
	}
	entries, err := parsePreparedCheckoutIndex(index)
	if err != nil {
		return err
	}
	if err := rejectPreparedCheckoutFilters(ctx, repository, entries); err != nil {
		return err
	}

	var paths strings.Builder
	for _, entry := range entries {
		if entry.mode == preparedCheckoutGitlinkMode {
			continue
		}
		paths.WriteString(entry.path)
		paths.WriteByte(0)
	}
	if paths.Len() != 0 {
		output, checkoutErr := repository.runPrivateGitAtKeepingOutput(
			ctx,
			".",
			paths.String(),
			"checkout-index",
			"--temp",
			"--stdin",
			"-z",
		)
		tempNames := preparedCheckoutTempNames(output)
		defer func() {
			result = errors.Join(
				result,
				removePreparedCheckoutTemps(repository.gitRoot, tempNames),
			)
		}()
		if checkoutErr != nil {
			return checkoutErr
		}
		if err := bindPreparedCheckoutTemps(entries, output); err != nil {
			return err
		}
	}

	for _, entry := range entries {
		switch entry.mode {
		case preparedCheckoutRegularMode, preparedCheckoutExecutableMode:
			if err := installPreparedCheckoutFile(
				repository.gitRoot,
				worktreeRoot,
				entry,
			); err != nil {
				return err
			}
		case preparedCheckoutSymlinkMode:
			if err := installPreparedCheckoutSymlink(
				ctx,
				repository,
				worktreeRoot,
				entry,
			); err != nil {
				return err
			}
		case preparedCheckoutGitlinkMode:
			if err := installPreparedCheckoutGitlink(
				worktreeRoot,
				entry.path,
			); err != nil {
				return err
			}
		}
	}
	directory, err := openRecordDirectory(worktreeRoot)
	if err != nil {
		return err
	}
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func parsePreparedCheckoutIndex(raw []byte) ([]*preparedCheckoutEntry, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, errors.New(
			"workspace: prepared index returned an incomplete entry",
		)
	}
	records := bytes.Split(raw[:len(raw)-1], []byte{0})
	entries := make([]*preparedCheckoutEntry, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		header, rawPath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, errors.New(
				"workspace: prepared index returned an invalid entry",
			)
		}
		fields := strings.Fields(string(header))
		if len(fields) != 3 || fields[2] != "0" {
			return nil, errors.New(
				"workspace: prepared index returned an unsupported stage",
			)
		}
		switch fields[0] {
		case preparedCheckoutRegularMode,
			preparedCheckoutExecutableMode,
			preparedCheckoutSymlinkMode,
			preparedCheckoutGitlinkMode:
		default:
			return nil, fmt.Errorf(
				"workspace: prepared index mode %q is unsupported",
				fields[0],
			)
		}
		if err := validatePreparedCheckoutOID(fields[1]); err != nil {
			return nil, err
		}
		path, err := validatePreparedCheckoutPath(string(rawPath))
		if err != nil {
			return nil, err
		}
		if _, exists := seen[path]; exists {
			return nil, fmt.Errorf(
				"workspace: prepared index path %q is duplicated",
				path,
			)
		}
		seen[path] = struct{}{}
		entries = append(entries, &preparedCheckoutEntry{
			mode: fields[0],
			oid:  fields[1],
			path: path,
		})
	}
	return entries, nil
}

func rejectPreparedCheckoutFilters(
	ctx context.Context,
	repository repositoryCapability,
	entries []*preparedCheckoutEntry,
) error {
	expected := make(map[string]struct{})
	var paths strings.Builder
	for _, entry := range entries {
		if entry.mode != preparedCheckoutRegularMode &&
			entry.mode != preparedCheckoutExecutableMode {
			continue
		}
		expected[entry.path] = struct{}{}
		paths.WriteString(entry.path)
		paths.WriteByte(0)
	}
	if paths.Len() == 0 {
		return nil
	}
	output, err := repository.runPrivateGitAt(
		ctx,
		".",
		paths.String(),
		"check-attr",
		"--cached",
		"-z",
		"--stdin",
		"filter",
	)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect prepared checkout filters: %w",
			err,
		)
	}
	if len(output) == 0 || output[len(output)-1] != 0 {
		return errors.New(
			"workspace: prepared checkout filter query returned incomplete output",
		)
	}
	fields := bytes.Split(output[:len(output)-1], []byte{0})
	if len(fields)%3 != 0 || len(fields)/3 != len(expected) {
		return errors.New(
			"workspace: prepared checkout filter query returned invalid output",
		)
	}
	seen := make(map[string]struct{}, len(expected))
	for index := 0; index < len(fields); index += 3 {
		path, err := validatePreparedCheckoutPath(string(fields[index]))
		if err != nil {
			return err
		}
		if _, exists := expected[path]; !exists {
			return fmt.Errorf(
				"workspace: prepared checkout filter query returned unexpected path %q",
				path,
			)
		}
		if _, exists := seen[path]; exists {
			return fmt.Errorf(
				"workspace: prepared checkout filter query returned path %q twice",
				path,
			)
		}
		seen[path] = struct{}{}
		if string(fields[index+1]) != "filter" {
			return errors.New(
				"workspace: prepared checkout filter query returned an invalid attribute",
			)
		}
		value := string(fields[index+2])
		if value != "unspecified" && value != "unset" {
			return fmt.Errorf(
				"workspace: working-tree filter %q for %q is unsupported on this platform",
				value,
				path,
			)
		}
	}
	return nil
}

func validatePreparedCheckoutOID(oid string) error {
	if len(oid) != 40 && len(oid) != 64 {
		return errors.New(
			"workspace: prepared index returned an invalid object ID",
		)
	}
	if _, err := hex.DecodeString(oid); err != nil {
		return errors.New(
			"workspace: prepared index returned an invalid object ID",
		)
	}
	return nil
}

func validatePreparedCheckoutPath(path string) (string, error) {
	clean, err := validateIncludedPath(path)
	if err != nil || filepath.ToSlash(clean) != path {
		return "", fmt.Errorf(
			"workspace: prepared index path %q is invalid",
			path,
		)
	}
	first, _, _ := strings.Cut(path, "/")
	if strings.EqualFold(first, ".git") {
		return "", fmt.Errorf(
			"workspace: prepared index path %q targets Git metadata",
			path,
		)
	}
	return clean, nil
}

func preparedCheckoutTempNames(raw []byte) []string {
	seen := make(map[string]struct{})
	var names []string
	for _, record := range bytes.Split(raw, []byte{0}) {
		rawName, _, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			continue
		}
		name := string(rawName)
		if !validPreparedCheckoutTempName(name) {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func validPreparedCheckoutTempName(name string) bool {
	return filepath.Base(name) == name &&
		name != "." &&
		(strings.HasPrefix(name, ".merge_file_") ||
			strings.HasPrefix(name, ".merge_link_"))
}

func bindPreparedCheckoutTemps(
	entries []*preparedCheckoutEntry,
	raw []byte,
) error {
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		return errors.New(
			"workspace: prepared checkout returned incomplete output",
		)
	}
	byPath := make(map[string]*preparedCheckoutEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.path] = entry
	}
	seenTemps := make(map[string]struct{}, len(entries))
	for _, record := range bytes.Split(raw[:len(raw)-1], []byte{0}) {
		rawTemp, rawPath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return errors.New(
				"workspace: prepared checkout returned an invalid entry",
			)
		}
		temp := string(rawTemp)
		if !validPreparedCheckoutTempName(temp) {
			return errors.New(
				"workspace: prepared checkout returned an invalid temporary path",
			)
		}
		if _, exists := seenTemps[temp]; exists {
			return errors.New(
				"workspace: prepared checkout reused a temporary path",
			)
		}
		seenTemps[temp] = struct{}{}
		path, err := validatePreparedCheckoutPath(string(rawPath))
		if err != nil {
			return err
		}
		entry, exists := byPath[path]
		if !exists || entry.mode == preparedCheckoutGitlinkMode {
			return fmt.Errorf(
				"workspace: prepared checkout returned unexpected path %q",
				path,
			)
		}
		if entry.temp != "" {
			return fmt.Errorf(
				"workspace: prepared checkout returned path %q twice",
				path,
			)
		}
		if entry.mode == preparedCheckoutSymlinkMode &&
			!strings.HasPrefix(temp, ".merge_link_") {
			return fmt.Errorf(
				"workspace: prepared checkout path %q is not a symlink",
				path,
			)
		}
		if entry.mode != preparedCheckoutSymlinkMode &&
			!strings.HasPrefix(temp, ".merge_file_") {
			return fmt.Errorf(
				"workspace: prepared checkout path %q is not a regular file",
				path,
			)
		}
		entry.temp = temp
	}
	for _, entry := range entries {
		if entry.mode != preparedCheckoutGitlinkMode && entry.temp == "" {
			return fmt.Errorf(
				"workspace: prepared checkout omitted path %q",
				entry.path,
			)
		}
	}
	return nil
}

func removePreparedCheckoutTemps(root *os.Root, names []string) error {
	var result error
	for _, name := range names {
		info, err := root.Lstat(name)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			result = errors.Join(result, err)
			continue
		case !info.Mode().IsRegular() &&
			info.Mode()&os.ModeSymlink == 0:
			result = errors.Join(
				result,
				fmt.Errorf(
					"workspace: prepared checkout temporary path %q changed type",
					name,
				),
			)
			continue
		}
		result = errors.Join(result, root.Remove(name))
	}
	return result
}

func installPreparedCheckoutFile(
	gitRoot *os.Root,
	worktreeRoot *os.Root,
	entry *preparedCheckoutEntry,
) (result error) {
	sourceInfo, err := gitRoot.Lstat(entry.temp)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect prepared checkout source for %q: %w",
			entry.path,
			err,
		)
	}
	if !sourceInfo.Mode().IsRegular() ||
		sourceInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"workspace: prepared checkout source for %q is not a regular file",
			entry.path,
		)
	}
	input, err := gitRoot.Open(entry.temp)
	if err != nil {
		return fmt.Errorf(
			"workspace: open prepared checkout source for %q: %w",
			entry.path,
			err,
		)
	}
	defer func() {
		result = errors.Join(result, input.Close())
	}()
	openedInfo, err := input.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() ||
		!os.SameFile(sourceInfo, openedInfo) {
		return fmt.Errorf(
			"workspace: prepared checkout source for %q changed while opening",
			entry.path,
		)
	}

	parent, owned, err := openPreparedCheckoutParent(
		worktreeRoot,
		filepath.Dir(entry.path),
	)
	if err != nil {
		return err
	}
	if owned {
		defer func() {
			result = errors.Join(result, parent.Close())
		}()
	}
	name := filepath.Base(entry.path)
	mode := os.FileMode(0o666)
	if entry.mode == preparedCheckoutExecutableMode {
		mode = 0o777
	}
	output, err := parent.OpenFile(
		name,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		mode,
	)
	if err != nil {
		return fmt.Errorf(
			"workspace: create prepared checkout path %q: %w",
			entry.path,
			err,
		)
	}
	outputOpen := true
	installed := false
	defer func() {
		if !installed {
			result = errors.Join(
				result,
				removeRecordPathIfSame(parent, name, output),
			)
		}
		if outputOpen {
			result = errors.Join(result, output.Close())
		}
	}()

	copiedDigest := sha256.New()
	copied, err := io.Copy(io.MultiWriter(output, copiedDigest), input)
	if err != nil {
		return fmt.Errorf(
			"workspace: copy prepared checkout path %q: %w",
			entry.path,
			err,
		)
	}
	afterCopy, err := input.Stat()
	if err != nil {
		return err
	}
	if copied != openedInfo.Size() ||
		afterCopy.Size() != openedInfo.Size() ||
		afterCopy.Mode() != openedInfo.Mode() ||
		!afterCopy.ModTime().Equal(openedInfo.ModTime()) {
		return fmt.Errorf(
			"workspace: prepared checkout source for %q changed while copying",
			entry.path,
		)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return err
	}
	verifiedDigest := sha256.New()
	verified, err := io.Copy(verifiedDigest, input)
	if err != nil {
		return err
	}
	afterVerification, err := input.Stat()
	if err != nil {
		return err
	}
	if verified != openedInfo.Size() ||
		afterVerification.Size() != openedInfo.Size() ||
		afterVerification.Mode() != openedInfo.Mode() ||
		!afterVerification.ModTime().Equal(openedInfo.ModTime()) ||
		!bytes.Equal(copiedDigest.Sum(nil), verifiedDigest.Sum(nil)) {
		return fmt.Errorf(
			"workspace: prepared checkout source for %q changed while verifying",
			entry.path,
		)
	}
	if err := output.Sync(); err != nil {
		return err
	}
	outputInfo, err := output.Stat()
	if err != nil {
		return err
	}
	gotExecutable := outputInfo.Mode().Perm()&0o111 != 0
	wantExecutable := entry.mode == preparedCheckoutExecutableMode
	if gotExecutable != wantExecutable {
		return fmt.Errorf(
			"workspace: prepared checkout mode for %q does not match the index",
			entry.path,
		)
	}
	if err := output.Close(); err != nil {
		outputOpen = false
		installed = true
		return err
	}
	outputOpen = false
	installed = true
	directory, err := openRecordDirectory(parent)
	if err != nil {
		return err
	}
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func installPreparedCheckoutSymlink(
	ctx context.Context,
	repository repositoryCapability,
	worktreeRoot *os.Root,
	entry *preparedCheckoutEntry,
) (result error) {
	tempInfo, err := repository.gitRoot.Lstat(entry.temp)
	if err != nil {
		return err
	}
	if !tempInfo.Mode().IsRegular() &&
		tempInfo.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf(
			"workspace: prepared checkout source for %q has an invalid type",
			entry.path,
		)
	}
	target, err := repository.runPrivateGitAt(
		ctx,
		".",
		"",
		"cat-file",
		"blob",
		entry.oid,
	)
	if err != nil {
		return err
	}
	if bytes.IndexByte(target, 0) >= 0 {
		return fmt.Errorf(
			"workspace: prepared checkout symlink %q contains a null byte",
			entry.path,
		)
	}
	parent, owned, err := openPreparedCheckoutParent(
		worktreeRoot,
		filepath.Dir(entry.path),
	)
	if err != nil {
		return err
	}
	if owned {
		defer func() {
			result = errors.Join(result, parent.Close())
		}()
	}
	directory, err := openRecordDirectory(parent)
	if err != nil {
		return err
	}
	if err := unix.Symlinkat(
		string(target),
		int(directory.Fd()),
		filepath.Base(entry.path),
	); err != nil {
		_ = directory.Close()
		return fmt.Errorf(
			"workspace: create prepared checkout symlink %q: %w",
			entry.path,
			err,
		)
	}
	info, inspectErr := parent.Lstat(filepath.Base(entry.path))
	if inspectErr == nil && info.Mode()&os.ModeSymlink == 0 {
		inspectErr = errors.New("installed path is not a symlink")
	}
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(inspectErr, syncErr, closeErr); err != nil {
		return fmt.Errorf(
			"workspace: verify prepared checkout symlink %q: %w",
			entry.path,
			err,
		)
	}
	return nil
}

func installPreparedCheckoutGitlink(
	worktreeRoot *os.Root,
	path string,
) (result error) {
	parent, owned, err := openPreparedCheckoutParent(
		worktreeRoot,
		filepath.Dir(path),
	)
	if err != nil {
		return err
	}
	if owned {
		defer func() {
			result = errors.Join(result, parent.Close())
		}()
	}
	name := filepath.Base(path)
	if err := parent.Mkdir(name, 0o777); err != nil {
		return fmt.Errorf(
			"workspace: create prepared checkout gitlink %q: %w",
			path,
			err,
		)
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"workspace: prepared checkout gitlink %q is not a real directory",
			path,
		)
	}
	directory, err := openRecordDirectory(parent)
	if err != nil {
		return err
	}
	syncErr := syncRecordDirectory(directory)
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func openPreparedCheckoutParent(
	root *os.Root,
	path string,
) (_ *os.Root, _ bool, result error) {
	current := root
	owned := false
	defer func() {
		if result != nil && owned {
			result = errors.Join(result, current.Close())
		}
	}()
	for _, component := range strings.Split(
		path,
		string(filepath.Separator),
	) {
		if component == "." || component == "" {
			continue
		}
		info, err := current.Lstat(component)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := current.Mkdir(component, 0o777); err != nil {
				return nil, false, fmt.Errorf(
					"workspace: create prepared checkout directory %q: %w",
					component,
					err,
				)
			}
			directory, openErr := openRecordDirectory(current)
			if openErr != nil {
				return nil, false, openErr
			}
			syncErr := syncRecordDirectory(directory)
			closeErr := directory.Close()
			if err := errors.Join(syncErr, closeErr); err != nil {
				return nil, false, err
			}
			info, err = current.Lstat(component)
			if err != nil {
				return nil, false, err
			}
		case err != nil:
			return nil, false, err
		case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
			return nil, false, fmt.Errorf(
				"workspace: prepared checkout parent %q is not a real directory",
				component,
			)
		}
		next, err := openRealRootFromRoot(current, component)
		if err != nil {
			return nil, false, err
		}
		if owned {
			if err := current.Close(); err != nil {
				_ = next.Close()
				return nil, false, err
			}
		}
		current = next
		owned = true
	}
	return current, owned, nil
}
