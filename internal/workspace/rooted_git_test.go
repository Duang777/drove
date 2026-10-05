package workspace

import (
	"slices"
	"strings"
	"testing"
)

func TestBoundGitEnvironmentRemovesInheritedGitConfiguration(t *testing.T) {
	environment := boundGitEnvironment(
		[]string{
			"PATH=/usr/bin",
			"GIT_COMMON_DIR=/replacement",
			"git_index_file=/replacement/index",
			"HOME=/tmp/home",
		},
		".",
		"/source",
	)

	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "GIT_INDEX_FILE") ||
			strings.EqualFold(name, "GIT_COMMON_DIR") {
			t.Fatalf("bound environment retained %q", entry)
		}
	}
	for _, want := range []string{
		"PATH=/usr/bin",
		"HOME=/tmp/home",
		"GIT_DIR=.",
		"GIT_WORK_TREE=/source",
	} {
		if !slices.Contains(environment, want) {
			t.Fatalf("bound environment %q does not contain %q", environment, want)
		}
	}
}

func TestBoundGitEnvironmentWithCommonSetsExplicitCommonDirectory(
	t *testing.T,
) {
	environment := boundGitEnvironmentWithCommon(
		[]string{"GIT_COMMON_DIR=/replacement"},
		"/git",
		"/worktree",
		"/common",
	)
	for _, want := range []string{
		"GIT_DIR=/git",
		"GIT_WORK_TREE=/worktree",
		"GIT_COMMON_DIR=/common",
	} {
		if !slices.Contains(environment, want) {
			t.Fatalf("bound environment %q does not contain %q", environment, want)
		}
	}
}
