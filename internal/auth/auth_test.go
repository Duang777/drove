package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestControllerRotatesBearerGenerationAfterGrace(t *testing.T) {
	dataDir := t.TempDir()
	controller, err := Open(dataDir, Options{
		RotationGrace: 50 * time.Millisecond,
		LoginCodeTTL:  time.Minute,
		SessionTTL:    time.Minute,
	})
	if err != nil {
		t.Fatalf("open controller: %v", err)
	}
	oldToken, err := Read(TokenPath(dataDir))
	if err != nil {
		t.Fatalf("read old token: %v", err)
	}
	oldGrant, ok := controller.AuthorizeBearer("Bearer " + oldToken)
	if !ok || oldGrant.Kind() != BearerAccess {
		t.Fatal("old token was not authorized")
	}

	if err := controller.Rotate(); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	newToken, err := Read(TokenPath(dataDir))
	if err != nil {
		t.Fatalf("read new token: %v", err)
	}
	if newToken == oldToken {
		t.Fatal("rotation preserved the old token")
	}
	if _, ok := controller.AuthorizeBearer("Bearer " + oldToken); !ok {
		t.Fatal("old token was rejected during grace")
	}
	if _, ok := controller.AuthorizeBearer("Bearer " + newToken); !ok {
		t.Fatal("new token was rejected")
	}

	select {
	case <-oldGrant.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("old grant was not revoked after grace")
	}
	if _, ok := controller.AuthorizeBearer("Bearer " + oldToken); ok {
		t.Fatal("old token remained authorized after grace")
	}
}

func TestControllerSecondRotationRevokesOlderGeneration(t *testing.T) {
	dataDir := t.TempDir()
	controller, err := Open(dataDir, Options{
		RotationGrace: time.Minute,
		LoginCodeTTL:  time.Minute,
		SessionTTL:    time.Minute,
	})
	if err != nil {
		t.Fatalf("open controller: %v", err)
	}
	firstToken, err := Read(TokenPath(dataDir))
	if err != nil {
		t.Fatalf("read first token: %v", err)
	}
	firstGrant, ok := controller.AuthorizeBearer("Bearer " + firstToken)
	if !ok {
		t.Fatal("first token was not authorized")
	}
	if err := controller.Rotate(); err != nil {
		t.Fatalf("first rotation: %v", err)
	}
	if err := controller.Rotate(); err != nil {
		t.Fatalf("second rotation: %v", err)
	}

	select {
	case <-firstGrant.Done():
	case <-time.After(time.Second):
		t.Fatal("second rotation did not revoke the oldest generation")
	}
	if _, ok := controller.AuthorizeBearer("Bearer " + firstToken); ok {
		t.Fatal("oldest token remained authorized")
	}
}

func TestControllerExchangesOneTimeCodeForRevocableCookie(t *testing.T) {
	dataDir := t.TempDir()
	controller, err := Open(dataDir, Options{
		RotationGrace: 30 * time.Millisecond,
		LoginCodeTTL:  time.Minute,
		SessionTTL:    time.Minute,
	})
	if err != nil {
		t.Fatalf("open controller: %v", err)
	}
	code, err := controller.IssueLoginCode()
	if err != nil {
		t.Fatalf("issue login code: %v", err)
	}
	cookie, grant, err := controller.ExchangeLoginCode(code)
	if err != nil {
		t.Fatalf("exchange login code: %v", err)
	}
	if cookie == "" || grant.Kind() != CookieAccess {
		t.Fatalf("cookie grant = %q %+v", cookie, grant)
	}
	if _, _, err := controller.ExchangeLoginCode(code); !errors.Is(err, ErrInvalidLoginCode) {
		t.Fatalf("reuse login code error = %v, want ErrInvalidLoginCode", err)
	}
	authorized, ok := controller.AuthorizeCookie(cookie)
	if !ok || authorized.Kind() != CookieAccess {
		t.Fatal("cookie was not authorized")
	}

	if err := controller.Rotate(); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	select {
	case <-grant.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cookie grant was not revoked after rotation grace")
	}
	if _, ok := controller.AuthorizeCookie(cookie); ok {
		t.Fatal("cookie remained authorized after rotation grace")
	}
}

func TestControllerRejectsExpiredLoginCode(t *testing.T) {
	controller, err := Open(t.TempDir(), Options{
		RotationGrace: time.Minute,
		LoginCodeTTL:  10 * time.Millisecond,
		SessionTTL:    time.Minute,
	})
	if err != nil {
		t.Fatalf("open controller: %v", err)
	}
	code, err := controller.IssueLoginCode()
	if err != nil {
		t.Fatalf("issue login code: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, _, err := controller.ExchangeLoginCode(code); !errors.Is(err, ErrInvalidLoginCode) {
		t.Fatalf("expired login code error = %v, want ErrInvalidLoginCode", err)
	}
}
