//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
