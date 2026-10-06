//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package workspace

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
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
	temp *preparedCheckoutTemp
}

type preparedCheckoutTemp struct {
	name     string
	file     *os.File
	info     os.FileInfo
	verified bool
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
		if entry.mode != preparedCheckoutRegularMode &&
			entry.mode != preparedCheckoutExecutableMode {
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

func validPreparedCheckoutTempName(name string) bool {
	return filepath.Base(name) == name &&
		name != "." &&
		strings.HasPrefix(name, ".merge_file_")
}

func openPreparedCheckoutTemp(
	root *os.Root,
	temp *preparedCheckoutTemp,
) error {
	if temp == nil || temp.file != nil ||
		!validPreparedCheckoutTempName(temp.name) {
		return errors.New(
			"workspace: prepared checkout temporary handle is invalid",
		)
	}
	info, err := root.Lstat(temp.name)
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect prepared checkout temporary path %q: %w",
			temp.name,
			err,
		)
	}
	if !info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"workspace: prepared checkout temporary path %q is not a regular file",
			temp.name,
		)
	}
	file, err := root.Open(temp.name)
	if err != nil {
		return fmt.Errorf(
			"workspace: open prepared checkout temporary path %q: %w",
			temp.name,
			err,
		)
	}
	opened, err := file.Stat()
	if err != nil {
		return errors.Join(
			fmt.Errorf(
				"workspace: inspect opened prepared checkout temporary path %q: %w",
				temp.name,
				err,
			),
			file.Close(),
		)
	}
	if !opened.Mode().IsRegular() ||
		opened.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(info, opened) {
		return errors.Join(
			fmt.Errorf(
				"workspace: prepared checkout temporary path %q changed while opening",
				temp.name,
			),
			file.Close(),
		)
	}
	temp.file = file
	temp.info = opened
	return nil
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
		if entry.mode == preparedCheckoutRegularMode ||
			entry.mode == preparedCheckoutExecutableMode {
			byPath[entry.path] = entry
		}
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
		if !exists {
			return fmt.Errorf(
				"workspace: prepared checkout returned unexpected path %q",
				path,
			)
		}
		if entry.temp != nil {
			return fmt.Errorf(
				"workspace: prepared checkout returned path %q twice",
				path,
			)
		}
		entry.temp = &preparedCheckoutTemp{name: temp}
	}
	for _, entry := range entries {
		if (entry.mode == preparedCheckoutRegularMode ||
			entry.mode == preparedCheckoutExecutableMode) &&
			entry.temp == nil {
			return fmt.Errorf(
				"workspace: prepared checkout omitted path %q",
				entry.path,
			)
		}
	}
	return nil
}

func verifyPreparedCheckoutTemp(entry *preparedCheckoutEntry) error {
	if entry.temp == nil || entry.temp.file == nil {
		return fmt.Errorf(
			"workspace: prepared checkout source for %q is not open",
			entry.path,
		)
	}
	oid, err := preparedCheckoutTempBlobOID(
		entry.temp,
		entry.path,
		entry.oid,
	)
	if err != nil {
		return err
	}
	if !strings.EqualFold(oid, entry.oid) {
		return fmt.Errorf(
			"workspace: prepared checkout source for %q does not match the index",
			entry.path,
		)
	}
	entry.temp.verified = true
	return nil
}

func preparedCheckoutTempBlobOID(
	temp *preparedCheckoutTemp,
	path string,
	expectedOID string,
) (string, error) {
	before, err := temp.file.Stat()
	if err != nil {
		return "", fmt.Errorf(
			"workspace: inspect prepared checkout source for %q: %w",
			path,
			err,
		)
	}
	if !samePreparedCheckoutTempState(temp.info, before) {
		return "", fmt.Errorf(
			"workspace: prepared checkout source for %q changed before reading",
			path,
		)
	}
	if _, err := temp.file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf(
			"workspace: rewind prepared checkout source for %q: %w",
			path,
			err,
		)
	}
	digest, err := newPreparedCheckoutBlobHash(expectedOID, before.Size())
	if err != nil {
		return "", err
	}
	read, err := io.Copy(digest, temp.file)
	if err != nil {
		return "", fmt.Errorf(
			"workspace: hash prepared checkout source for %q: %w",
			path,
			err,
		)
	}
	after, err := temp.file.Stat()
	if err != nil {
		return "", fmt.Errorf(
			"workspace: inspect prepared checkout source for %q after reading: %w",
			path,
			err,
		)
	}
	if read != before.Size() ||
		!samePreparedCheckoutTempState(before, after) ||
		!samePreparedCheckoutTempState(temp.info, after) {
		return "", fmt.Errorf(
			"workspace: prepared checkout source for %q changed while reading",
			path,
		)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func newPreparedCheckoutBlobHash(
	expectedOID string,
	size int64,
) (hash.Hash, error) {
	var digest hash.Hash
	switch len(expectedOID) {
	case sha1.Size * 2:
		digest = sha1.New()
	case sha256.Size * 2:
		digest = sha256.New()
	default:
		return nil, errors.New(
			"workspace: prepared checkout object ID uses an unsupported hash",
		)
	}
	header := fmt.Sprintf("blob %d\x00", size)
	if _, err := io.WriteString(digest, header); err != nil {
		return nil, fmt.Errorf(
			"workspace: initialize prepared checkout blob hash: %w",
			err,
		)
	}
	return digest, nil
}

func samePreparedCheckoutTempState(
	expected os.FileInfo,
	current os.FileInfo,
) bool {
	return expected.Mode().IsRegular() &&
		current.Mode().IsRegular() &&
		expected.Mode()&os.ModeSymlink == 0 &&
		current.Mode()&os.ModeSymlink == 0 &&
		os.SameFile(expected, current) &&
		expected.Size() == current.Size() &&
		expected.Mode() == current.Mode() &&
		expected.ModTime().Equal(current.ModTime())
}

func removePreparedCheckoutTemps(
	root *os.Root,
	temps []*preparedCheckoutTemp,
) error {
	var result error
	for _, temp := range temps {
		if temp == nil || temp.file == nil {
			continue
		}
		if temp.verified {
			result = errors.Join(
				result,
				removeOwnedRecordPathIfSame(
					root,
					temp.name,
					temp.file,
					".drove-checkout-temp-"+uuid.NewString(),
					nil,
				),
			)
		}
		result = errors.Join(result, temp.file.Close())
	}
	return result
}

func installPreparedCheckoutFile(
	gitRoot *os.Root,
	worktreeRoot *os.Root,
	entry *preparedCheckoutEntry,
) (result error) {
	if entry.temp == nil {
		return fmt.Errorf(
			"workspace: prepared checkout source for %q is unavailable",
			entry.path,
		)
	}
	if err := openPreparedCheckoutTemp(gitRoot, entry.temp); err != nil {
		return err
	}
	defer func() {
		result = errors.Join(
			result,
			removePreparedCheckoutTemps(
				gitRoot,
				[]*preparedCheckoutTemp{entry.temp},
			),
		)
	}()
	if err := verifyPreparedCheckoutTemp(entry); err != nil {
		return err
	}
	input := entry.temp.file
	sourceInfo, err := input.Stat()
	if err != nil {
		return fmt.Errorf(
			"workspace: inspect prepared checkout source for %q: %w",
			entry.path,
			err,
		)
	}
	if !samePreparedCheckoutTempState(entry.temp.info, sourceInfo) {
		return fmt.Errorf(
			"workspace: prepared checkout source for %q changed before copying",
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
				removeOwnedRecordPathIfSame(
					parent,
					name,
					output,
					".drove-checkout-discard-"+uuid.NewString(),
					nil,
				),
			)
		}
		if outputOpen {
			result = errors.Join(result, output.Close())
		}
	}()

	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf(
			"workspace: rewind prepared checkout source for %q: %w",
			entry.path,
			err,
		)
	}
	copiedDigest, err := newPreparedCheckoutBlobHash(
		entry.oid,
		sourceInfo.Size(),
	)
	if err != nil {
		return err
	}
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
	copiedOID := hex.EncodeToString(copiedDigest.Sum(nil))
	if copied != sourceInfo.Size() ||
		!samePreparedCheckoutTempState(sourceInfo, afterCopy) ||
		!samePreparedCheckoutTempState(entry.temp.info, afterCopy) ||
		!strings.EqualFold(copiedOID, entry.oid) {
		return fmt.Errorf(
			"workspace: prepared checkout source for %q changed or did not match the index while copying",
			entry.path,
		)
	}
	verifiedOID, err := preparedCheckoutTempBlobOID(
		entry.temp,
		entry.path,
		entry.oid,
	)
	if err != nil {
		return err
	}
	if !strings.EqualFold(verifiedOID, entry.oid) ||
		!strings.EqualFold(verifiedOID, copiedOID) {
		return fmt.Errorf(
			"workspace: prepared checkout source for %q did not match the index while verifying",
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
