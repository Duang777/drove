package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseRemovalAcknowledgementReservationName(t *testing.T) {
	const (
		prefix = ".record.ack-path-operation-"
		id     = "abcdefab-cdef-4abc-8def-abcdefabcdef"
	)
	for _, test := range []struct {
		name    string
		value   string
		valid   bool
		renamed bool
	}{
		{name: "base", value: prefix + id, valid: true},
		{
			name:    "rename",
			value:   prefix + id + ".rename",
			valid:   true,
			renamed: true,
		},
		{name: "wrong prefix", value: "other-" + id},
		{name: "uppercase UUID", value: prefix + strings.ToUpper(id)},
		{name: "double rename", value: prefix + id + ".rename.rename"},
		{name: "extra suffix", value: prefix + id + ".extra"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate, valid := parseRemovalAcknowledgementReservationName(
				test.value,
				prefix,
			)
			if valid != test.valid {
				t.Fatalf("valid = %v, want %v", valid, test.valid)
			}
			if valid && candidate.renamed != test.renamed {
				t.Fatalf(
					"renamed = %v, want %v",
					candidate.renamed,
					test.renamed,
				)
			}
		})
	}
}

func TestCleanupRemovalAcknowledgementReservationsRecoversRenamePhase(
	t *testing.T,
) {
	for _, renamed := range []bool{false, true} {
		t.Run(map[bool]string{false: "base", true: "rename"}[renamed], func(t *testing.T) {
			bucketPath := t.TempDir()
			bucket, err := os.OpenRoot(bucketPath)
			if err != nil {
				t.Fatalf("open bucket: %v", err)
			}
			defer bucket.Close()
			record := removalAcknowledgementTestRecord(bucketPath)
			name := removalAcknowledgementReservationPrefix(record) +
				"89898989-8989-4898-8989-898989898989"
			createRemovalAcknowledgementReservationFixture(
				t,
				bucket,
				record,
				name,
			)
			if renamed {
				renamedPath := filepath.Join(bucketPath, name+".rename")
				if err := os.Rename(
					filepath.Join(bucketPath, name),
					renamedPath,
				); err != nil {
					t.Fatalf("isolate reservation: %v", err)
				}
				name += ".rename"
			}

			if err := cleanupRemovalAcknowledgementReservations(
				bucket,
				record,
			); err != nil {
				t.Fatalf("cleanup reservation: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(bucketPath, name)); !errors.Is(
				err,
				os.ErrNotExist,
			) {
				t.Fatalf("reservation remains: %v", err)
			}
			if err := cleanupRemovalAcknowledgementReservations(
				bucket,
				record,
			); err != nil {
				t.Fatalf("repeat cleanup reservation: %v", err)
			}
		})
	}
}

func TestCleanupRemovalAcknowledgementReservationsRecoversMarkerDiscard(
	t *testing.T,
) {
	bucketPath := t.TempDir()
	bucket, err := os.OpenRoot(bucketPath)
	if err != nil {
		t.Fatalf("open bucket: %v", err)
	}
	defer bucket.Close()
	record := removalAcknowledgementTestRecord(bucketPath)
	name := removalAcknowledgementReservationPrefix(record) +
		"91919191-9191-4191-8191-919191919191"
	createRemovalAcknowledgementReservationFixture(
		t,
		bucket,
		record,
		name,
	)
	reservationPath := filepath.Join(bucketPath, name)
	if err := os.Rename(
		filepath.Join(reservationPath, removalMarkerName(record)),
		filepath.Join(reservationPath, removalMarkerDiscardName(record)),
	); err != nil {
		t.Fatalf("isolate removal marker: %v", err)
	}

	if err := cleanupRemovalAcknowledgementReservations(
		bucket,
		record,
	); err != nil {
		t.Fatalf("cleanup reservation: %v", err)
	}
	if _, err := os.Lstat(reservationPath); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("reservation remains: %v", err)
	}
}

func TestCleanupRemovalAcknowledgementReservationsRejectsAmbiguousPhases(
	t *testing.T,
) {
	bucketPath := t.TempDir()
	bucket, err := os.OpenRoot(bucketPath)
	if err != nil {
		t.Fatalf("open bucket: %v", err)
	}
	defer bucket.Close()
	record := removalAcknowledgementTestRecord(bucketPath)
	baseName := removalAcknowledgementReservationPrefix(record) +
		"90909090-9090-4090-8090-909090909090"
	for _, name := range []string{baseName, baseName + ".rename"} {
		createRemovalAcknowledgementReservationFixture(
			t,
			bucket,
			record,
			name,
		)
	}

	if err := cleanupRemovalAcknowledgementReservations(
		bucket,
		record,
	); err == nil {
		t.Fatal("cleanup accepted ambiguous reservation phases")
	}
	for _, name := range []string{baseName, baseName + ".rename"} {
		if _, err := os.Stat(filepath.Join(bucketPath, name)); err != nil {
			t.Fatalf("ambiguous reservation %q changed: %v", name, err)
		}
	}
}

func removalAcknowledgementTestRecord(bucketPath string) workspaceRecord {
	return workspaceRecord{
		AgentID: testAgentID,
		Path:    filepath.Join(bucketPath, testAgentID),
		Removal: &workspaceRemovalRecord{
			OperationID:    "67676767-6767-4767-8767-676767676767",
			DirectoryToken: "68686868-6868-4868-8868-686868686868",
			Started:        true,
		},
	}
}

func createRemovalAcknowledgementReservationFixture(
	t *testing.T,
	bucket *os.Root,
	record workspaceRecord,
	name string,
) {
	t.Helper()
	if err := bucket.Mkdir(name, 0o700); err != nil {
		t.Fatalf("create reservation %q: %v", name, err)
	}
	root, err := openRealRootFromRoot(bucket, name)
	if err != nil {
		t.Fatalf("open reservation %q: %v", name, err)
	}
	if err := installRemovalMarker(
		root,
		removalMarkerName(record),
		record.Removal.DirectoryToken,
	); err != nil {
		_ = root.Close()
		t.Fatalf("install reservation marker: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close reservation %q: %v", name, err)
	}
}

func TestAcknowledgeRemovalReservesManagedPathDuringRepositoryCheck(
	t *testing.T,
) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires a POSIX shell")
	}
	repository := newTestRepository(t)
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
	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find Git: %v", err)
	}
	hookMarker := filepath.Join(t.TempDir(), "reservation-observed")
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := `#!/bin/sh
"$DROVE_TEST_REAL_GIT" "$@"
status=$?
case " $* " in
  *" worktree list --porcelain -z "*)
    if mkdir "$DROVE_TEST_WORKSPACE_PATH" 2>/dev/null; then
      exit 91
    fi
    [ -d "$DROVE_TEST_WORKSPACE_PATH" ] || exit 92
    : > "$DROVE_TEST_HOOK_MARKER"
    ;;
esac
exit "$status"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write Git wrapper: %v", err)
	}
	t.Setenv("DROVE_TEST_REAL_GIT", realGit)
	t.Setenv("DROVE_TEST_WORKSPACE_PATH", prepared.Path)
	t.Setenv("DROVE_TEST_HOOK_MARKER", hookMarker)
	manager.git = wrapper

	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
	}
	if _, err := os.Stat(hookMarker); err != nil {
		t.Fatalf("repository check did not observe reservation: %v", err)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acknowledgement reservation remains: %v", err)
	}
	if _, err := os.Lstat(workspaceRecordPath(prepared.Path)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("acknowledged record remains: %v", err)
	}
}

func TestAcknowledgeRemovalRecoversManagedPathReservation(
	t *testing.T,
) {
	repository := newTestRepository(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	manager, err := New(dataDir)
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
	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		true,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove workspace = %+v, %v", result, err)
	}
	record, exists, err := manager.readWorkspaceRecord(prepared.Path)
	if err != nil || !exists {
		t.Fatalf("read removal record: exists=%v err=%v", exists, err)
	}
	reservation, err := manager.reserveRemovalAcknowledgementPath(record)
	if err != nil {
		t.Fatalf("reserve acknowledgement path: %v", err)
	}
	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list acknowledgement reservation: %v", err)
	}
	if len(listed) != 1 || !listed[0].Missing {
		t.Fatalf("listed acknowledgement reservation = %+v", listed)
	}
	if err := reservation.root.Close(); err != nil {
		t.Fatalf("close reservation root: %v", err)
	}
	reservation.root = nil
	if err := reservation.bucket.Close(); err != nil {
		t.Fatalf("close reservation bucket: %v", err)
	}
	reservation.bucket = nil

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	removals, err := restarted.ReconcileRemovals(context.Background())
	if err != nil {
		t.Fatalf("reconcile removal with reservation: %v", err)
	}
	if len(removals) != 1 ||
		removals[0].operationID != result.Removal.operationID {
		t.Fatalf("reconciled removals = %+v", removals)
	}
	if err := restarted.AcknowledgeRemoval(removals[0]); err != nil {
		t.Fatalf("acknowledge recovered reservation: %v", err)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered acknowledgement reservation remains: %v", err)
	}
}
