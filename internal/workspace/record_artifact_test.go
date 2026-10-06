package workspace

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverWorkspaceRecordTemporaryArtifact(t *testing.T) {
	manager, target, _, dataDir := newRecordArtifactFixture(t)
	recordPath := workspaceRecordPath(target.Path)
	recordName := filepath.Base(recordPath)
	temporaryName := workspaceRecordTemporaryName(recordName)
	temporaryPath := filepath.Join(filepath.Dir(recordPath), temporaryName)
	if err := os.Link(recordPath, temporaryPath); err != nil {
		t.Fatalf("link temporary record: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	if err := restarted.recoverRecordAcknowledgements(); err != nil {
		t.Fatalf("recover record artifacts: %v", err)
	}
	if err := restarted.recoverRecordAcknowledgements(); err != nil {
		t.Fatalf("repeat record artifact recovery: %v", err)
	}
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("canonical record changed: %v", err)
	}
	if _, err := os.Lstat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary record remains: %v", err)
	}
	_ = manager
}

func TestRecoverWorkspaceRecordTemporaryDebrisBesideCanonical(
	t *testing.T,
) {
	for _, test := range []struct {
		name    string
		content func([]byte) []byte
	}{
		{
			name: "complete separate inode",
			content: func(canonical []byte) []byte {
				return append([]byte(nil), canonical...)
			},
		},
		{
			name: "partial write",
			content: func([]byte) []byte {
				return []byte("{")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, target, _, dataDir := newRecordArtifactFixture(t)
			recordPath := workspaceRecordPath(target.Path)
			canonical, err := os.ReadFile(recordPath)
			if err != nil {
				t.Fatalf("read canonical record: %v", err)
			}
			temporaryPath := filepath.Join(
				filepath.Dir(recordPath),
				workspaceRecordTemporaryName(filepath.Base(recordPath)),
			)
			if err := os.WriteFile(
				temporaryPath,
				test.content(canonical),
				0o600,
			); err != nil {
				t.Fatalf("write temporary record debris: %v", err)
			}

			restarted, err := New(dataDir)
			if err != nil {
				t.Fatalf("restart manager: %v", err)
			}
			if err := restarted.recoverRecordAcknowledgements(); err != nil {
				t.Fatalf("recover record artifacts: %v", err)
			}
			current, err := os.ReadFile(recordPath)
			if err != nil {
				t.Fatalf("read recovered canonical record: %v", err)
			}
			if !bytes.Equal(current, canonical) {
				t.Fatal("temporary debris changed the canonical record")
			}
			if _, err := os.Lstat(temporaryPath); !errors.Is(
				err,
				os.ErrNotExist,
			) {
				t.Fatalf("temporary record debris remains: %v", err)
			}
		})
	}
}

func TestRecoverWorkspaceRecordRemovalArtifact(t *testing.T) {
	_, target, record, dataDir := newRecordArtifactFixture(t)
	recordPath := workspaceRecordPath(target.Path)
	removalName := workspaceRecordRemovalName(record)
	removalPath := filepath.Join(filepath.Dir(recordPath), removalName)
	if err := os.Rename(recordPath, removalPath); err != nil {
		t.Fatalf("isolate removal record: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	if err := restarted.recoverRecordAcknowledgements(); err != nil {
		t.Fatalf("recover record artifacts: %v", err)
	}
	if err := restarted.recoverRecordAcknowledgements(); err != nil {
		t.Fatalf("repeat record artifact recovery: %v", err)
	}
	for _, path := range []string{recordPath, removalPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("record artifact %q remains: %v", path, err)
		}
	}
}

func TestRecoverWorkspaceRecordArtifactsRejectsLegacyRemoval(t *testing.T) {
	_, target, _, dataDir := newRecordArtifactFixture(t)
	recordPath := workspaceRecordPath(target.Path)
	legacyName := workspaceRecordRemovalLegacyPrefix +
		"78787878-7878-4787-8787-787878787878"
	legacyPath := filepath.Join(filepath.Dir(recordPath), legacyName)
	if err := os.Rename(recordPath, legacyPath); err != nil {
		t.Fatalf("isolate legacy removal record: %v", err)
	}

	restarted, err := New(dataDir)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	if err := restarted.recoverRecordAcknowledgements(); err == nil {
		t.Fatal("recovery accepted a legacy removal artifact")
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy removal artifact changed: %v", err)
	}
}

func TestRemoveWorkspaceRecordLeavesNoUnixTransactionDebris(t *testing.T) {
	manager, target, _, _ := newRecordArtifactFixture(t)
	if err := manager.removeWorkspaceRecord(target.Path); err != nil {
		t.Fatalf("remove workspace record: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(target.Path))
	if err != nil {
		t.Fatalf("read record bucket: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == target.AgentID+workspaceRecordSuffix {
			t.Fatalf("canonical record remains: %q", entry.Name())
		}
		if _, valid := parseWorkspaceRecordArtifactName(entry.Name()); valid {
			t.Fatalf("record artifact remains: %q", entry.Name())
		}
	}
}

func newRecordArtifactFixture(
	t *testing.T,
) (*Manager, Workspace, workspaceRecord, string) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	manager, err := New(dataDir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if err := manager.ensureManagedRoot(); err != nil {
		t.Fatalf("ensure managed root: %v", err)
	}
	if err := manager.ensureManagedBucket(repository); err != nil {
		t.Fatalf("ensure managed bucket: %v", err)
	}
	target := Workspace{
		AgentID:    testAgentID,
		Repository: repository,
		Path: filepath.Join(
			manager.root,
			repositoryHash(repository),
			testAgentID,
		),
		Branch: "record-artifact",
	}
	if err := manager.writeWorkspaceRecord(target, nil); err != nil {
		t.Fatalf("write workspace record: %v", err)
	}
	record, exists, err := manager.readWorkspaceRecord(target.Path)
	if err != nil || !exists {
		t.Fatalf("read workspace record: exists=%v err=%v", exists, err)
	}
	return manager, target, record, dataDir
}
