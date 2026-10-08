package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func TestLoadOrCreateVAPIDPersistsPrivateCredentialsAtomically(t *testing.T) {
	dataDir := t.TempDir()
	first, err := LoadOrCreateVAPID(dataDir)
	if err != nil {
		t.Fatalf("create VAPID credentials: %v", err)
	}
	if err := first.validate(); err != nil {
		t.Fatalf("validate generated credentials: %v", err)
	}

	dir := filepath.Join(dataDir, "notify")
	assertPathMode(t, dir, true, 0o700)
	path := filepath.Join(dir, "vapid.json")
	assertPathMode(t, path, false, 0o600)
	matches, err := filepath.Glob(filepath.Join(dir, ".vapid-*.tmp"))
	if err != nil {
		t.Fatalf("glob temporary VAPID files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary VAPID files remain: %v", matches)
	}

	second, err := LoadOrCreateVAPID(dataDir)
	if err != nil {
		t.Fatalf("reload VAPID credentials: %v", err)
	}
	if second != first {
		t.Fatal("reload generated a different VAPID key pair")
	}
}

func TestLoadOrCreateVAPIDRejectsUnsafePathsAndModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX permission bits consistently")
	}
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{
			name: "open directory mode",
			prepare: func(t *testing.T, dataDir string) {
				t.Helper()
				dir := filepath.Join(dataDir, "notify")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatalf("create notify directory: %v", err)
				}
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatalf("set notify directory mode: %v", err)
				}
			},
			want: "want 0700",
		},
		{
			name: "credentials directory is a file",
			prepare: func(t *testing.T, dataDir string) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(dataDir, "notify"),
					[]byte("not a directory"),
					0o600,
				); err != nil {
					t.Fatalf("write notify path: %v", err)
				}
			},
			want: "must be a directory",
		},
		{
			name: "open credential file mode",
			prepare: func(t *testing.T, dataDir string) {
				t.Helper()
				writeTestVAPID(t, dataDir, 0o644, false)
			},
			want: "want 0600",
		},
		{
			name: "credential path is a directory",
			prepare: func(t *testing.T, dataDir string) {
				t.Helper()
				dir := filepath.Join(dataDir, "notify")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatalf("create notify directory: %v", err)
				}
				if err := os.Mkdir(filepath.Join(dir, "vapid.json"), 0o700); err != nil {
					t.Fatalf("create credential directory: %v", err)
				}
			},
			want: "must be a regular file",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			test.prepare(t, dataDir)
			_, err := LoadOrCreateVAPID(dataDir)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadOrCreateVAPIDRejectsMalformedOrMismatchedKeys(t *testing.T) {
	tests := []struct {
		name       string
		mismatched bool
		raw        string
		want       string
	}{
		{
			name: "unknown JSON field",
			raw: `{"public_key":"x","private_key":"y","extra":true}
`,
			want: "unknown field",
		},
		{
			name: "invalid encoding",
			raw: `{"public_key":"x","private_key":"y"}
`,
			want: "private key is invalid",
		},
		{
			name:       "mismatched pair",
			mismatched: true,
			want:       "does not match",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			dir := filepath.Join(dataDir, "notify")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatalf("create notify directory: %v", err)
			}
			raw := test.raw
			if test.mismatched {
				firstPrivate, _, err := webpush.GenerateVAPIDKeys()
				if err != nil {
					t.Fatalf("generate first VAPID key: %v", err)
				}
				_, secondPublic, err := webpush.GenerateVAPIDKeys()
				if err != nil {
					t.Fatalf("generate second VAPID key: %v", err)
				}
				encoded, err := json.Marshal(VAPIDCredentials{
					PublicKey:  secondPublic,
					PrivateKey: firstPrivate,
				})
				if err != nil {
					t.Fatalf("encode mismatched VAPID keys: %v", err)
				}
				raw = string(encoded)
			}
			if err := os.WriteFile(
				filepath.Join(dir, "vapid.json"),
				[]byte(raw),
				0o600,
			); err != nil {
				t.Fatalf("write VAPID credentials: %v", err)
			}
			_, err := LoadOrCreateVAPID(dataDir)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load error = %v, want %q", err, test.want)
			}
		})
	}
}

func assertPathMode(
	t *testing.T,
	path string,
	wantDir bool,
	wantMode os.FileMode,
) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect %s: %v", path, err)
	}
	if info.IsDir() != wantDir {
		t.Fatalf("%s directory = %t, want %t", path, info.IsDir(), wantDir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != wantMode {
		t.Fatalf("%s mode = %04o, want %04o", path, info.Mode().Perm(), wantMode)
	}
}

func writeTestVAPID(
	t *testing.T,
	dataDir string,
	mode os.FileMode,
	mismatch bool,
) {
	t.Helper()
	dir := filepath.Join(dataDir, "notify")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create notify directory: %v", err)
	}
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("generate VAPID key: %v", err)
	}
	if mismatch {
		_, publicKey, err = webpush.GenerateVAPIDKeys()
		if err != nil {
			t.Fatalf("generate mismatched VAPID key: %v", err)
		}
	}
	raw, err := json.Marshal(VAPIDCredentials{
		PublicKey:  publicKey,
		PrivateKey: privateKey,
	})
	if err != nil {
		t.Fatalf("encode VAPID credentials: %v", err)
	}
	path := filepath.Join(dir, "vapid.json")
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatalf("write VAPID credentials: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("set VAPID mode: %v", err)
		}
	}
}
