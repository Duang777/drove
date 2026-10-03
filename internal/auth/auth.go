// Package auth manages the local control-plane credential shared by the daemon
// and trusted clients.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// TokenFileName is the control token file within the configured data directory.
	TokenFileName = "control.token"
	tokenBytes    = 32
	tokenLength   = tokenBytes * 2
)

// TokenPath returns the control token path for dataDir.
func TokenPath(dataDir string) string {
	return filepath.Join(dataDir, TokenFileName)
}

// Ensure reads the existing control token or atomically creates one.
func Ensure(dataDir string) (string, error) {
	if dataDir == "" {
		return "", errors.New("auth: data directory must not be empty")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("auth: create data directory: %w", err)
	}

	path := TokenPath(dataDir)
	token, err := Read(path)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generate control token: %w", err)
	}
	token = hex.EncodeToString(raw)

	temp, err := os.CreateTemp(dataDir, "."+TokenFileName+"-*")
	if err != nil {
		return "", fmt.Errorf("auth: create temporary token: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("auth: secure temporary token: %w", err)
	}
	if _, err := io.WriteString(temp, token); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("auth: write temporary token: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("auth: sync temporary token: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("auth: close temporary token: %w", err)
	}

	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Read(path)
		}
		return "", fmt.Errorf("auth: publish control token: %w", err)
	}
	return token, nil
}

// Read loads and validates a control token file.
func Read(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("auth: inspect control token: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("auth: control token must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("auth: control token permissions are %04o, want 0600", info.Mode().Perm())
	}

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("auth: open control token: %w", err)
	}
	defer file.Close()

	openedInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("auth: stat open control token: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		return "", errors.New("auth: control token changed while opening")
	}

	raw, err := io.ReadAll(io.LimitReader(file, tokenLength+1))
	if err != nil {
		return "", fmt.Errorf("auth: read control token: %w", err)
	}
	if len(raw) != tokenLength {
		return "", fmt.Errorf("auth: control token has %d bytes, want %d", len(raw), tokenLength)
	}

	decoded := make([]byte, tokenBytes)
	if _, err := hex.Decode(decoded, raw); err != nil ||
		hex.EncodeToString(decoded) != string(raw) {
		return "", errors.New("auth: control token is not lowercase hexadecimal")
	}
	return string(raw), nil
}

// Verify reports whether authorization contains the expected Bearer token.
func Verify(token, authorization string) bool {
	if token == "" {
		return false
	}
	presented, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok || len(presented) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(presented)) == 1
}
