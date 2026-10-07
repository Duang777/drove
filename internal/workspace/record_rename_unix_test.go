//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRenameRecordRejectsSourceReplacementAfterValidation(t *testing.T) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	source, err := os.OpenFile(
		sourcePath,
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	defer source.Close()
	if _, err := source.WriteString("original\n"); err != nil {
		t.Fatalf("write source: %v", err)
	}
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer directory.Close()
	movedPath := filepath.Join(rootPath, "source-original")

	installed, err := renameRecordPathAfterValidation(
		directory,
		source,
		"source",
		"target",
		false,
		func() {
			if err := os.Rename(sourcePath, movedPath); err != nil {
				t.Fatalf("move validated source: %v", err)
			}
			if err := os.WriteFile(
				sourcePath,
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				t.Fatalf("install replacement source: %v", err)
			}
		},
	)
	if err == nil || installed {
		t.Fatalf(
			"rename replacement = installed %v, err=%v",
			installed,
			err,
		)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, "target")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("target exists after rejected replacement: %v", err)
	}
	assertFileContents(t, sourcePath, "replacement\n")
	assertFileContents(t, movedPath, "original\n")
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		t.Fatalf("read record directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".drove-install-") {
			t.Fatalf("staged alias remains after rejected replacement: %q", entry.Name())
		}
	}
}

func TestUnlinkRecordPreservesReplacementAfterValidation(t *testing.T) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	source, err := os.OpenFile(
		sourcePath,
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	defer source.Close()
	if _, err := source.WriteString("original\n"); err != nil {
		t.Fatalf("write source: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()
	movedPath := filepath.Join(rootPath, "source-original")

	err = removeOwnedRecordPath(
		root,
		"source",
		source,
		"source-owned-removal",
		nil,
		func() {
			if err := os.Rename(sourcePath, movedPath); err != nil {
				t.Fatalf("move validated source: %v", err)
			}
			if err := os.WriteFile(
				sourcePath,
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				t.Fatalf("install replacement source: %v", err)
			}
		},
	)
	if err == nil {
		t.Fatal("unlink accepted a replacement source")
	}
	assertFileContents(t, sourcePath, "replacement\n")
	assertFileContents(t, movedPath, "original\n")
}

func TestUnlinkRecordPreservesReplacementAfterFinalValidation(t *testing.T) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	source, err := os.OpenFile(
		sourcePath,
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	defer source.Close()
	if _, err := source.WriteString("original\n"); err != nil {
		t.Fatalf("write source: %v", err)
	}
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer directory.Close()
	movedPath := filepath.Join(rootPath, "source-original")

	err = unlinkRecordPathAfterValidation(
		directory,
		source,
		"source",
		func(name string) {
			path := filepath.Join(rootPath, name)
			if err := os.Rename(path, movedPath); err != nil {
				t.Fatalf("move validated source: %v", err)
			}
			if err := os.WriteFile(
				path,
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				t.Fatalf("install replacement source: %v", err)
			}
		},
	)
	if err == nil {
		t.Fatal("unlink accepted a final replacement")
	}
	assertFileContents(t, sourcePath, "replacement\n")
	assertFileContents(t, movedPath, "original\n")
}

func TestUnlinkLinkedRecordPreservesReplacementAfterFinalValidation(
	t *testing.T,
) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	if err := os.WriteFile(sourcePath, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	targetPath := filepath.Join(rootPath, "target")
	if err := os.Link(sourcePath, targetPath); err != nil {
		t.Fatalf("link target witness: %v", err)
	}
	expected, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer expected.Close()
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer directory.Close()
	var replacementPath, movedPath string

	err = unlinkLinkedRecordPathAfterValidation(
		directory,
		expected,
		"source",
		"target",
		func(name string) {
			replacementPath = filepath.Join(rootPath, name)
			movedPath = replacementPath + ".original"
			if err := os.Rename(replacementPath, movedPath); err != nil {
				t.Fatalf("move validated linked source: %v", err)
			}
			if err := os.WriteFile(
				replacementPath,
				[]byte("replacement\n"),
				0o600,
			); err != nil {
				t.Fatalf("install replacement linked source: %v", err)
			}
		},
	)
	if err == nil {
		t.Fatal("linked unlink accepted a final replacement")
	}
	assertFileContents(t, replacementPath, "replacement\n")
	assertFileContents(t, movedPath, "original\n")
	assertFileContents(t, targetPath, "original\n")
}

func TestRecoverRecordDeletionRestoresReplacement(t *testing.T) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	if err := os.WriteFile(sourcePath, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	expected, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer expected.Close()
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer directory.Close()
	namespace, err := openRecordDeletionNamespace(directory)
	if err != nil {
		t.Fatalf("open deletion namespace: %v", err)
	}
	transactionName := "12121212-1212-4121-8121-121212121212"
	transaction, err := createRecordDeletionTransaction(
		namespace,
		transactionName,
		"source",
	)
	if err != nil {
		t.Fatalf("create deletion transaction: %v", err)
	}
	if err := unix.Linkat(
		int(directory.Fd()),
		"source",
		int(transaction.Fd()),
		recordDeletionWitness,
		0,
	); err != nil {
		t.Fatalf("link deletion witness: %v", err)
	}
	if err := verifyRecordPathIdentity(
		transaction,
		expected,
		recordDeletionWitness,
	); err != nil {
		t.Fatalf("verify deletion witness: %v", err)
	}
	if err := markRecordDeletionValidated(transaction); err != nil {
		t.Fatalf("mark deletion validated: %v", err)
	}
	movedPath := filepath.Join(rootPath, "source-original")
	if err := os.Rename(sourcePath, movedPath); err != nil {
		t.Fatalf("move validated source: %v", err)
	}
	if err := os.WriteFile(
		sourcePath,
		[]byte("replacement\n"),
		0o600,
	); err != nil {
		t.Fatalf("install replacement source: %v", err)
	}
	if err := unix.Renameat(
		int(directory.Fd()),
		"source",
		int(transaction.Fd()),
		recordDeletionCandidate,
	); err != nil {
		t.Fatalf("move replacement candidate: %v", err)
	}
	if err := transaction.Close(); err != nil {
		t.Fatalf("close deletion transaction: %v", err)
	}
	if err := namespace.Close(); err != nil {
		t.Fatalf("close deletion namespace: %v", err)
	}

	namespace, err = openRecordDeletionNamespace(directory)
	if err != nil {
		t.Fatalf("reopen deletion namespace: %v", err)
	}
	defer namespace.Close()
	if err := recoverRecordDeletionTransactions(
		directory,
		namespace,
	); err != nil {
		t.Fatalf("recover deletion transaction: %v", err)
	}
	assertFileContents(t, sourcePath, "replacement\n")
	assertFileContents(t, movedPath, "original\n")
	entries, err := readRecordDeletionDirectory(namespace)
	if err != nil {
		t.Fatalf("read recovered deletion namespace: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("recovered deletion artifacts remain: %v", entries)
	}
}

func TestRecoverRecordDeletionCompletesValidatedCleanup(t *testing.T) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	if err := os.WriteFile(sourcePath, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer directory.Close()
	namespace, err := openRecordDeletionNamespace(directory)
	if err != nil {
		t.Fatalf("open deletion namespace: %v", err)
	}
	defer namespace.Close()
	transactionName := "34343434-3434-4343-8343-343434343434"
	transaction, err := createRecordDeletionTransaction(
		namespace,
		transactionName,
		"source",
	)
	if err != nil {
		t.Fatalf("create deletion transaction: %v", err)
	}
	if err := unix.Linkat(
		int(directory.Fd()),
		"source",
		int(transaction.Fd()),
		recordDeletionWitness,
		0,
	); err != nil {
		t.Fatalf("link deletion witness: %v", err)
	}
	if err := markRecordDeletionValidated(transaction); err != nil {
		t.Fatalf("mark deletion validated: %v", err)
	}
	if err := unix.Renameat(
		int(directory.Fd()),
		"source",
		int(transaction.Fd()),
		recordDeletionCandidate,
	); err != nil {
		t.Fatalf("move deletion candidate: %v", err)
	}
	if err := unlinkPrivateRecordPath(
		transaction,
		recordDeletionCandidate,
	); err != nil {
		t.Fatalf("remove deletion candidate: %v", err)
	}
	if err := unlinkPrivateRecordPath(
		transaction,
		recordDeletionWitness,
	); err != nil {
		t.Fatalf("remove deletion witness: %v", err)
	}
	if err := transaction.Sync(); err != nil {
		t.Fatalf("sync interrupted deletion transaction: %v", err)
	}
	if err := transaction.Close(); err != nil {
		t.Fatalf("close deletion transaction: %v", err)
	}

	if err := recoverRecordDeletionTransactions(
		directory,
		namespace,
	); err != nil {
		t.Fatalf("recover validated deletion cleanup: %v", err)
	}
	if _, err := os.Lstat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted source returned: %v", err)
	}
	entries, err := readRecordDeletionDirectory(namespace)
	if err != nil {
		t.Fatalf("read recovered deletion namespace: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("recovered deletion artifacts remain: %v", entries)
	}
}

func TestRecoverRecordDeletionRemovesEmptyTransaction(t *testing.T) {
	rootPath := t.TempDir()
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer directory.Close()
	namespace, err := openRecordDeletionNamespace(directory)
	if err != nil {
		t.Fatalf("open deletion namespace: %v", err)
	}
	defer namespace.Close()
	transactionName := "56565656-5656-4565-8565-565656565656"
	if err := unix.Mkdirat(
		int(namespace.Fd()),
		transactionName,
		0o700,
	); err != nil {
		t.Fatalf("create empty deletion transaction: %v", err)
	}
	if err := namespace.Sync(); err != nil {
		t.Fatalf("sync empty deletion transaction: %v", err)
	}

	if err := recoverRecordDeletionTransactions(
		directory,
		namespace,
	); err != nil {
		t.Fatalf("recover empty deletion transaction: %v", err)
	}
	entries, err := readRecordDeletionDirectory(namespace)
	if err != nil {
		t.Fatalf("read recovered deletion namespace: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("empty deletion transaction remains: %v", entries)
	}
}

func TestRecoverRecordRenameDebrisSettlesDeletionNamespace(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open record root: %v", err)
	}
	defer root.Close()
	directory, err := openRecordDirectory(root)
	if err != nil {
		t.Fatalf("open record directory: %v", err)
	}
	namespace, err := openRecordDeletionNamespace(directory)
	if err != nil {
		t.Fatalf("open deletion namespace: %v", err)
	}
	transaction, err := createRecordDeletionTransaction(
		namespace,
		"67676767-6767-4767-8767-676767676767",
		"interrupted-record",
	)
	if err != nil {
		t.Fatalf("create deletion transaction: %v", err)
	}
	if err := errors.Join(
		transaction.Close(),
		namespace.Close(),
		directory.Close(),
	); err != nil {
		t.Fatalf("close deletion transaction: %v", err)
	}

	if err := recoverRecordRenameDebris(root); err != nil {
		t.Fatalf("recover deletion namespace: %v", err)
	}
	if _, err := root.Lstat(recordDeletionNamespace); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("deletion namespace remains: %v", err)
	}
}

func TestRemoveRecoversRecordDeletionNamespaceBeforeStatus(t *testing.T) {
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
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := manager.AcknowledgePreparation(prepared); err != nil {
		t.Fatalf("acknowledge preparation: %v", err)
	}
	root, err := os.OpenRoot(prepared.Path)
	if err != nil {
		t.Fatalf("open workspace root: %v", err)
	}
	directory, err := openRecordDirectory(root)
	if err != nil {
		t.Fatalf("open workspace directory: %v", err)
	}
	namespace, err := openRecordDeletionNamespace(directory)
	if err != nil {
		t.Fatalf("open workspace deletion namespace: %v", err)
	}
	transaction, err := createRecordDeletionTransaction(
		namespace,
		"89898989-8989-4898-8989-898989898989",
		"interrupted-marker",
	)
	if err != nil {
		t.Fatalf("create marker deletion transaction: %v", err)
	}
	if err := errors.Join(
		transaction.Close(),
		namespace.Close(),
		directory.Close(),
		root.Close(),
	); err != nil {
		t.Fatalf("close marker deletion transaction: %v", err)
	}
	status := runGit(
		t,
		prepared.Path,
		"status",
		"--porcelain=v1",
		"--untracked-files=all",
	)
	if !strings.Contains(status, recordDeletionNamespace) {
		t.Fatalf("Git status did not observe deletion namespace: %q", status)
	}

	result, err := manager.Remove(
		context.Background(),
		prepared.AgentID,
		false,
	)
	if err != nil || result.State != RemovalComplete {
		t.Fatalf("remove after deletion recovery = %+v, %v", result, err)
	}
	if err := manager.AcknowledgeRemoval(result.Removal); err != nil {
		t.Fatalf("acknowledge removal: %v", err)
	}
}

func TestUnlinkLinkedRecordPathAcceptsMissingSourceWithMatchingWitness(
	t *testing.T,
) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	if err := os.WriteFile(sourcePath, []byte("record\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	targetPath := filepath.Join(rootPath, "target")
	if err := os.Link(sourcePath, targetPath); err != nil {
		t.Fatalf("link target witness: %v", err)
	}
	expected, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer expected.Close()
	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	directory, err := os.Open(rootPath)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer directory.Close()

	if err := unlinkLinkedRecordPath(
		directory,
		expected,
		"source",
		"target",
	); err != nil {
		t.Fatalf("accept missing linked source: %v", err)
	}
	assertFileContents(t, targetPath, "record\n")
}

func TestRecoverRecordInstallAliasUsesSameFileWitness(t *testing.T) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	if err := os.WriteFile(sourcePath, []byte("record\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	aliasName := recordInstallAliasPrefix +
		"78787878-7878-4787-8787-787878787878"
	aliasPath := filepath.Join(rootPath, aliasName)
	if err := os.Link(sourcePath, aliasPath); err != nil {
		t.Fatalf("link install alias: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	if err := recoverRecordRenameDebris(root); err != nil {
		t.Fatalf("recover install alias: %v", err)
	}
	if err := recoverRecordRenameDebris(root); err != nil {
		t.Fatalf("repeat install alias recovery: %v", err)
	}
	assertFileContents(t, sourcePath, "record\n")
	if _, err := os.Lstat(aliasPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install alias remains: %v", err)
	}
	if _, err := root.Lstat(recordDeletionNamespace); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("record deletion namespace remains: %v", err)
	}
}

func TestRecoverRecordInstallAliasRejectsMissingWitness(t *testing.T) {
	rootPath := t.TempDir()
	aliasName := recordInstallAliasPrefix +
		"89898989-8989-4898-8989-898989898989"
	aliasPath := filepath.Join(rootPath, aliasName)
	if err := os.WriteFile(aliasPath, []byte("record\n"), 0o600); err != nil {
		t.Fatalf("write install alias: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	if err := recoverRecordRenameDebris(root); err == nil {
		t.Fatal("recovery accepted an install alias without an owner")
	}
	assertFileContents(t, aliasPath, "record\n")
}

func TestRecoverRecordRenameDebrisRejectsLegacyRemovalBeforeMutation(
	t *testing.T,
) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source")
	if err := os.WriteFile(sourcePath, []byte("record\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	aliasName := recordInstallAliasPrefix +
		"90909090-9090-4090-8090-909090909090"
	aliasPath := filepath.Join(rootPath, aliasName)
	if err := os.Link(sourcePath, aliasPath); err != nil {
		t.Fatalf("link install alias: %v", err)
	}
	legacyName := recordRemoveLegacyPrefix +
		"67676767-6767-4767-8767-676767676767"
	legacyPath := filepath.Join(rootPath, legacyName)
	if err := os.WriteFile(legacyPath, []byte("legacy\n"), 0o600); err != nil {
		t.Fatalf("write legacy removal artifact: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	if err := recoverRecordRenameDebris(root); err == nil {
		t.Fatal("recovery accepted a legacy removal artifact")
	}
	assertFileContents(t, aliasPath, "record\n")
	assertFileContents(t, legacyPath, "legacy\n")
}
