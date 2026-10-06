//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPreparedCheckoutMaterializesTrackedEntryTypes(t *testing.T) {
	repository := newTestRepository(t)
	executable := filepath.Join(repository, "run.sh")
	if err := os.WriteFile(
		executable,
		[]byte("#!/bin/sh\nexit 0\n"),
		0o700,
	); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	if err := os.Symlink(
		"tracked.txt",
		filepath.Join(repository, "relative-link"),
	); err != nil {
		t.Skipf("create relative symlink: %v", err)
	}
	absoluteTarget := filepath.Join(t.TempDir(), "outside")
	if err := os.Symlink(
		absoluteTarget,
		filepath.Join(repository, "absolute-link"),
	); err != nil {
		t.Skipf("create absolute symlink: %v", err)
	}
	runGit(
		t,
		repository,
		"add",
		"run.sh",
		"relative-link",
		"absolute-link",
	)
	runGit(t, repository, "update-index", "--chmod=+x", "run.sh")
	runGit(t, repository, "commit", "-m", "add tracked entry types")
	head := strings.TrimSpace(runGitOutput(t, repository, "rev-parse", "HEAD"))
	runGit(
		t,
		repository,
		"update-index",
		"--add",
		"--cacheinfo",
		"160000,"+head+",modules/sub",
	)
	runGit(t, repository, "commit", "-m", "add gitlink")

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare worktree: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(prepared.Path, "tracked.txt"))
	if err != nil {
		t.Fatalf("read tracked file: %v", err)
	}
	if string(content) != "tracked\n" {
		t.Fatalf("tracked file = %q", content)
	}
	info, err := os.Stat(filepath.Join(prepared.Path, "run.sh"))
	if err != nil {
		t.Fatalf("inspect executable: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("executable mode = %v", info.Mode())
	}
	relative, err := os.Readlink(
		filepath.Join(prepared.Path, "relative-link"),
	)
	if err != nil {
		t.Fatalf("read relative symlink: %v", err)
	}
	if relative != "tracked.txt" {
		t.Fatalf("relative symlink = %q", relative)
	}
	absolute, err := os.Readlink(
		filepath.Join(prepared.Path, "absolute-link"),
	)
	if err != nil {
		t.Fatalf("read absolute symlink: %v", err)
	}
	if absolute != absoluteTarget {
		t.Fatalf("absolute symlink = %q", absolute)
	}
	gitlink, err := os.Lstat(
		filepath.Join(prepared.Path, "modules", "sub"),
	)
	if err != nil {
		t.Fatalf("inspect gitlink: %v", err)
	}
	if !gitlink.IsDir() || gitlink.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("gitlink mode = %v", gitlink.Mode())
	}
}

func TestPreparedCheckoutRejectsWorkingTreeFilter(t *testing.T) {
	repository := newTestRepository(t)
	sentinel := filepath.Join(t.TempDir(), "filter-ran")
	filter := filepath.Join(t.TempDir(), "filter")
	if err := os.WriteFile(
		filter,
		[]byte("#!/bin/sh\nprintf ran > \"$DROVE_TEST_SENTINEL\"\ncat\n"),
		0o700,
	); err != nil {
		t.Fatalf("write smudge filter: %v", err)
	}
	t.Setenv("DROVE_TEST_SENTINEL", sentinel)
	runGit(t, repository, "config", "filter.review.smudge", filter)
	runGit(t, repository, "config", "filter.review.clean", "cat")
	runGit(t, repository, "config", "filter.review.required", "true")
	if err := os.WriteFile(
		filepath.Join(repository, ".gitattributes"),
		[]byte("tracked.txt filter=review\n"),
		0o600,
	); err != nil {
		t.Fatalf("write attributes: %v", err)
	}
	runGit(t, repository, "add", ".gitattributes")
	runGit(t, repository, "commit", "-m", "add checkout filter")

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if _, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	); err == nil || !strings.Contains(
		err.Error(),
		"working-tree filter",
	) {
		t.Fatalf("prepare filtered worktree error = %v", err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected smudge filter ran: %v", err)
	}
}

func TestRemovePreparedCheckoutTempsPreservesReplacement(t *testing.T) {
	path := t.TempDir()
	const (
		name     = ".merge_file_owned"
		original = ".merge_file_owned.original"
		content  = "trusted\n"
	)
	if err := os.WriteFile(
		filepath.Join(path, name),
		[]byte(content),
		0o600,
	); err != nil {
		t.Fatalf("write prepared checkout temporary: %v", err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatalf("open temporary root: %v", err)
	}
	defer root.Close()
	temp := &preparedCheckoutTemp{name: name}
	if err := openPreparedCheckoutTemp(root, temp); err != nil {
		t.Fatalf("open prepared checkout temporary: %v", err)
	}
	temps := []*preparedCheckoutTemp{temp}
	closed := false
	defer func() {
		if !closed {
			for _, temp := range temps {
				_ = temp.file.Close()
			}
		}
	}()
	entry := &preparedCheckoutEntry{
		mode: preparedCheckoutRegularMode,
		oid:  "3136b9e8f996037b1178065c3d107c5053690d7f",
		path: "tracked.txt",
		temp: temps[0],
	}
	if err := verifyPreparedCheckoutTemp(entry); err != nil {
		t.Fatalf("verify prepared checkout temporary: %v", err)
	}
	if err := os.Rename(
		filepath.Join(path, name),
		filepath.Join(path, original),
	); err != nil {
		t.Fatalf("rename original temporary: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(path, name),
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("write replacement temporary: %v", err)
	}
	err = removePreparedCheckoutTemps(root, temps)
	closed = true
	if err == nil || !strings.Contains(err.Error(), "changed identity") {
		t.Fatalf("remove replaced prepared checkout temporary error = %v", err)
	}
	assertFileContents(t, filepath.Join(path, name), "replacement\n")
	assertFileContents(t, filepath.Join(path, original), content)
}

func TestPreparedCheckoutKeepsOpenFilesBounded(t *testing.T) {
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &original); err != nil {
		t.Skipf("read open file limit: %v", err)
	}
	if original.Cur < 64 {
		t.Skipf("open file limit %d is already below test limit", original.Cur)
	}
	limited := original
	limited.Cur = 64
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &limited); err != nil {
		t.Skipf("lower open file limit: %v", err)
	}
	defer func() {
		if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &original); err != nil {
			t.Errorf("restore open file limit: %v", err)
		}
	}()

	repository := newTestRepository(t)
	for index := range 96 {
		name := filepath.Join(repository, fmt.Sprintf("tracked-%03d.txt", index))
		if err := os.WriteFile(name, []byte("tracked\n"), 0o600); err != nil {
			t.Fatalf("write tracked file %d: %v", index, err)
		}
	}
	runGit(t, repository, "add", "--all")
	runGit(t, repository, "commit", "-m", "add many tracked files")

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if _, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	); err != nil {
		t.Fatalf("prepare worktree with bounded open files: %v", err)
	}
}

func TestPreparedCheckoutSupportsSHA256Objects(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatalf("create SHA-256 repository: %v", err)
	}
	command := exec.Command(
		"git",
		"-C",
		repository,
		"init",
		"--object-format=sha256",
		"--initial-branch=main",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("Git does not support SHA-256 repositories: %v\n%s", err, output)
	}
	runGit(t, repository, "config", "user.name", "Drove Test")
	runGit(t, repository, "config", "user.email", "drove@example.invalid")
	if err := os.WriteFile(
		filepath.Join(repository, "tracked.txt"),
		[]byte("sha256\n"),
		0o600,
	); err != nil {
		t.Fatalf("write SHA-256 tracked file: %v", err)
	}
	runGit(t, repository, "add", "tracked.txt")
	runGit(t, repository, "commit", "-m", "initial")

	manager, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	prepared, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	)
	if err != nil {
		t.Fatalf("prepare SHA-256 worktree: %v", err)
	}
	assertFileContents(
		t,
		filepath.Join(prepared.Path, "tracked.txt"),
		"sha256\n",
	)
}

func TestParsePreparedCheckoutIndexRejectsInvalidEntries(t *testing.T) {
	validOID := strings.Repeat("0", 40)
	tests := map[string]string{
		"incomplete":       preparedCheckoutRegularMode + " " + validOID + " 0\tfile",
		"nonzero stage":    preparedCheckoutRegularMode + " " + validOID + " 1\tfile\x00",
		"unsupported mode": "100600 " + validOID + " 0\tfile\x00",
		"invalid oid":      preparedCheckoutRegularMode + " xyz 0\tfile\x00",
		"escaping path":    preparedCheckoutRegularMode + " " + validOID + " 0\t../file\x00",
		"git metadata":     preparedCheckoutRegularMode + " " + validOID + " 0\t.GIT/config\x00",
		"duplicate path": preparedCheckoutRegularMode + " " + validOID + " 0\tfile\x00" +
			preparedCheckoutRegularMode + " " + validOID + " 0\tfile\x00",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePreparedCheckoutIndex([]byte(raw)); err == nil {
				t.Fatal("invalid prepared index entry was accepted")
			}
		})
	}
}

func runGitOutput(
	t *testing.T,
	repository string,
	arguments ...string,
) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf(
				"git %v: %v\n%s",
				arguments,
				err,
				exitErr.Stderr,
			)
		}
		t.Fatalf("git %v: %v", arguments, err)
	}
	return string(output)
}
