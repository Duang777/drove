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

func TestPreparedCheckoutRejectsReplacedGitTemporary(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	initSourceSwapRepository(t, repository)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	outputFile := filepath.Join(parent, "checkout-output")
	wrapper := filepath.Join(parent, "git-wrapper-checkout-temp")
	script := `#!/bin/sh
checkout=
for argument in "$@"; do
  [ "$argument" = "checkout-index" ] && checkout=1
done
if [ -n "$checkout" ]; then
  "$DROVE_TEST_REAL_GIT" "$@" > "$DROVE_TEST_CHECKOUT_OUTPUT"
  status=$?
  [ "$status" -eq 0 ] || exit "$status"
  temp=$(
    tr '\000' '\n' < "$DROVE_TEST_CHECKOUT_OUTPUT" |
      awk -F '	' 'NF >= 2 { print $1; exit }'
  ) || exit 90
  [ -n "$temp" ] || exit 91
  original=$temp.original
  mv "$temp" "$original" || exit 92
  printf 'replacement\n' > "$temp" || exit 93
  cat "$DROVE_TEST_CHECKOUT_OUTPUT"
  exit 0
fi
exec "$DROVE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_CHECKOUT_OUTPUT", outputFile)

	dataDir := filepath.Join(parent, "data")
	manager, err := New(dataDir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.git = wrapper
	if _, err := manager.Prepare(
		context.Background(),
		repository,
		"",
		testAgentID,
	); err == nil || !strings.Contains(
		err.Error(),
		"does not match the index",
	) {
		t.Fatalf("prepare after temporary replacement error = %v", err)
	}

	canonicalRepository, err := resolvePath(repository)
	if err != nil {
		t.Fatalf("resolve repository: %v", err)
	}
	target := filepath.Join(
		manager.root,
		repositoryHash(canonicalRepository),
		testAgentID,
	)
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement bytes reached a prepared worktree: %v", err)
	}
}
