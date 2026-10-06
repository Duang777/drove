package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupRemovalMarkerDiscardIsIdempotent(t *testing.T) {
	record := removalMarkerArtifactTestRecord()
	path := t.TempDir()
	name := removalMarkerDiscardName(record)
	if err := os.WriteFile(
		filepath.Join(path, name),
		[]byte("partial\n"),
		0o600,
	); err != nil {
		t.Fatalf("write marker discard: %v", err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatalf("open marker root: %v", err)
	}
	defer root.Close()

	if err := cleanupRemovalMarkerTemps(root, record); err != nil {
		t.Fatalf("cleanup marker discard: %v", err)
	}
	if err := cleanupRemovalMarkerTemps(root, record); err != nil {
		t.Fatalf("repeat marker discard cleanup: %v", err)
	}
	if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker discard remains: %v", err)
	}
}

func TestRemoveRemovalMarkerRecoversDiscardedMarker(t *testing.T) {
	record := removalMarkerArtifactTestRecord()
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatalf("open marker root: %v", err)
	}
	defer root.Close()
	if err := installRemovalMarker(
		root,
		removalMarkerName(record),
		record.Removal.DirectoryToken,
	); err != nil {
		t.Fatalf("install removal marker: %v", err)
	}
	discardName := removalMarkerDiscardName(record)
	if err := os.Rename(
		filepath.Join(path, removalMarkerName(record)),
		filepath.Join(path, discardName),
	); err != nil {
		t.Fatalf("isolate removal marker: %v", err)
	}

	if err := removeRemovalMarkerIfPresent(root, record); err != nil {
		t.Fatalf("recover discarded marker: %v", err)
	}
	if _, err := root.Lstat(discardName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker discard remains: %v", err)
	}
}

func removalMarkerArtifactTestRecord() workspaceRecord {
	return workspaceRecord{
		AgentID: testAgentID,
		Removal: &workspaceRemovalRecord{
			OperationID:    "78787878-7878-4787-8787-787878787878",
			DirectoryToken: "89898989-8989-4898-8989-898989898989",
		},
	}
}
