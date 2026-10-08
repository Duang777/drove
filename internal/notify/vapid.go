package notify

import (
	"bytes"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"

	webpush "github.com/SherClockHolmes/webpush-go"
)

const maxVAPIDFileBytes = 4 * 1024

// VAPIDCredentials are the application-server keys used for Web Push.
type VAPIDCredentials struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

// LoadOrCreateVAPID loads or atomically creates private Web Push credentials.
func LoadOrCreateVAPID(dataDir string) (VAPIDCredentials, error) {
	dir := filepath.Join(dataDir, "notify")
	if err := ensurePrivateDirectory(dir); err != nil {
		return VAPIDCredentials{}, err
	}
	path := filepath.Join(dir, "vapid.json")
	credentials, found, err := loadVAPID(path)
	if err != nil {
		return VAPIDCredentials{}, err
	}
	if found {
		return credentials, nil
	}

	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return VAPIDCredentials{}, fmt.Errorf("notify: generate VAPID credentials: %w", err)
	}
	credentials = VAPIDCredentials{
		PublicKey:  publicKey,
		PrivateKey: privateKey,
	}
	if err := credentials.validate(); err != nil {
		return VAPIDCredentials{}, err
	}
	if err := writeVAPIDAtomically(path, credentials); err != nil {
		return VAPIDCredentials{}, err
	}
	return credentials, nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("notify: VAPID directory %q must be a directory", path)
		}
		if info.Mode().Perm() != 0o700 {
			return fmt.Errorf(
				"notify: VAPID directory %q permissions are %04o, want 0700",
				path,
				info.Mode().Perm(),
			)
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("notify: inspect VAPID directory %q: %w", path, err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("notify: create VAPID directory %q: %w", path, err)
	}
	return nil
}

func loadVAPID(path string) (VAPIDCredentials, bool, error) {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		return VAPIDCredentials{}, false, nil
	default:
		return VAPIDCredentials{}, false, fmt.Errorf(
			"notify: inspect VAPID credentials %q: %w",
			path,
			err,
		)
	}
	if !info.Mode().IsRegular() {
		return VAPIDCredentials{}, false, fmt.Errorf(
			"notify: VAPID credentials %q must be a regular file",
			path,
		)
	}
	if info.Mode().Perm() != 0o600 {
		return VAPIDCredentials{}, false, fmt.Errorf(
			"notify: VAPID credentials %q permissions are %04o, want 0600",
			path,
			info.Mode().Perm(),
		)
	}
	if info.Size() > maxVAPIDFileBytes {
		return VAPIDCredentials{}, false, fmt.Errorf(
			"notify: VAPID credentials %q exceed %d bytes",
			path,
			maxVAPIDFileBytes,
		)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return VAPIDCredentials{}, false, fmt.Errorf(
			"notify: read VAPID credentials %q: %w",
			path,
			err,
		)
	}
	var credentials VAPIDCredentials
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credentials); err != nil {
		return VAPIDCredentials{}, false, fmt.Errorf(
			"notify: parse VAPID credentials %q: %w",
			path,
			err,
		)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return VAPIDCredentials{}, false, fmt.Errorf(
			"notify: parse VAPID credentials %q: %w",
			path,
			err,
		)
	}
	if err := credentials.validate(); err != nil {
		return VAPIDCredentials{}, false, fmt.Errorf(
			"notify: validate VAPID credentials %q: %w",
			path,
			err,
		)
	}
	return credentials, true, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (c VAPIDCredentials) validate() error {
	privateKey, err := base64.RawURLEncoding.DecodeString(c.PrivateKey)
	if err != nil || len(privateKey) != 32 {
		return errors.New("notify: VAPID private key is invalid")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(c.PublicKey)
	if err != nil || len(publicKey) != 65 {
		return errors.New("notify: VAPID public key is invalid")
	}

	curve := elliptic.P256()
	x, y := elliptic.Unmarshal(curve, publicKey)
	if x == nil || y == nil || !curve.IsOnCurve(x, y) {
		return errors.New("notify: VAPID public key is invalid")
	}
	scalar := new(big.Int).SetBytes(privateKey)
	if scalar.Sign() <= 0 || scalar.Cmp(curve.Params().N) >= 0 {
		return errors.New("notify: VAPID private key is invalid")
	}
	expectedX, expectedY := curve.ScalarBaseMult(privateKey)
	if x.Cmp(expectedX) != 0 || y.Cmp(expectedY) != 0 {
		return errors.New("notify: VAPID key pair does not match")
	}
	return nil
}

func writeVAPIDAtomically(path string, credentials VAPIDCredentials) (err error) {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".vapid-*.tmp")
	if err != nil {
		return fmt.Errorf("notify: create temporary VAPID credentials: %w", err)
	}
	tempPath := file.Name()
	defer func() {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		if removeErr := os.Remove(tempPath); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(
				err,
				fmt.Errorf("notify: remove temporary VAPID credentials: %w", removeErr),
			)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("notify: secure temporary VAPID credentials: %w", err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(credentials); err != nil {
		return fmt.Errorf("notify: encode VAPID credentials: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("notify: sync VAPID credentials: %w", err)
	}
	if err := file.Close(); err != nil {
		file = nil
		return fmt.Errorf("notify: close VAPID credentials: %w", err)
	}
	file = nil
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("notify: install VAPID credentials: %w", err)
	}
	return nil
}
