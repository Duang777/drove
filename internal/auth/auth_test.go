package auth

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEnsureCreatesStablePrivateToken(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")

	first, err := Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure first token: %v", err)
	}
	second, err := Ensure(dataDir)
	if err != nil {
		t.Fatalf("ensure second token: %v", err)
	}
	if first != second {
		t.Fatalf("token changed across calls")
	}
	if len(first) != tokenLength {
		t.Fatalf("token length = %d, want %d", len(first), tokenLength)
	}

	info, err := os.Stat(TokenPath(dataDir))
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %04o, want 0600", info.Mode().Perm())
	}
	info, err = os.Lstat(dataDir)
	if err != nil {
		t.Fatalf("stat data directory: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("data directory mode = %04o, want 0700", info.Mode().Perm())
	}
}

func TestEnsureConcurrentCreationPublishesOneToken(t *testing.T) {
	dataDir := t.TempDir()
	const workers = 32

	start := make(chan struct{})
	results := make(chan string, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			<-start
			token, err := Ensure(dataDir)
			results <- token
			errs <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("ensure token: %v", err)
		}
	}
	stored, err := Read(TokenPath(dataDir))
	if err != nil {
		t.Fatalf("read stored token: %v", err)
	}
	for token := range results {
		if token != stored {
			t.Fatal("concurrent caller observed an unpublished token")
		}
	}
}

func TestReadRejectsUnsafeTokenFiles(t *testing.T) {
	valid := strings.Repeat("a", tokenLength)
	tests := []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "public permissions", content: valid, mode: 0o644},
		{name: "short value", content: "abcd", mode: 0o600},
		{name: "non hexadecimal", content: strings.Repeat("z", tokenLength), mode: 0o600},
		{name: "uppercase hexadecimal", content: strings.Repeat("A", tokenLength), mode: 0o600},
		{name: "trailing newline", content: valid + "\n", mode: 0o600},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), TokenFileName)
			if err := os.WriteFile(path, []byte(test.content), test.mode); err != nil {
				t.Fatalf("write token: %v", err)
			}
			if _, err := Read(path); err == nil {
				t.Fatal("read unsafe token succeeded")
			}
		})
	}
}

func TestReadRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(strings.Repeat("a", tokenLength)), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	path := filepath.Join(dir, TokenFileName)
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("read symlink token succeeded")
	}
}

func TestVerifyBearer(t *testing.T) {
	token := strings.Repeat("a", tokenLength)
	tests := []struct {
		name          string
		authorization string
		want          bool
	}{
		{name: "valid", authorization: "Bearer " + token, want: true},
		{name: "missing", authorization: "", want: false},
		{name: "wrong scheme", authorization: "Basic " + token, want: false},
		{name: "wrong token", authorization: "Bearer " + strings.Repeat("b", tokenLength), want: false},
		{name: "extra token", authorization: "Bearer " + token + "x", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Verify(token, test.authorization); got != test.want {
				t.Fatalf("Verify() = %t, want %t", got, test.want)
			}
		})
	}
}
