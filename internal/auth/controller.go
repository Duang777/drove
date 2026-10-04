package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const sessionBytes = 32

var (
	// ErrInvalidLoginCode indicates an unknown, expired, or already used login code.
	ErrInvalidLoginCode = errors.New("auth: invalid login code")
)

// AccessKind identifies the credential that produced a grant.
type AccessKind uint8

const (
	// BearerAccess is a control-token grant.
	BearerAccess AccessKind = iota + 1
	// CookieAccess is a browser-session grant.
	CookieAccess
)

// Options controls credential expiry and token-rotation behavior.
type Options struct {
	RotationGrace time.Duration
	LoginCodeTTL  time.Duration
	SessionTTL    time.Duration
}

// DefaultOptions returns the production credential lifetimes.
func DefaultOptions() Options {
	return Options{
		RotationGrace: 30 * time.Second,
		LoginCodeTTL:  2 * time.Minute,
		SessionTTL:    12 * time.Hour,
	}
}

type generation struct {
	token      string
	done       chan struct{}
	revokeOnce sync.Once
}

func newGeneration(token string) *generation {
	return &generation{token: token, done: make(chan struct{})}
}

func (g *generation) revoke() {
	g.revokeOnce.Do(func() {
		close(g.done)
	})
}

type loginCode struct {
	expires    time.Time
	generation *generation
}

type browserSession struct {
	expires    time.Time
	generation *generation
	done       chan struct{}
	revokeOnce sync.Once
}

func (s *browserSession) revoke() {
	s.revokeOnce.Do(func() {
		close(s.done)
	})
}

// Grant is a revocable authorization decision.
type Grant struct {
	kind AccessKind
	done <-chan struct{}
}

// Kind reports which credential produced the grant.
func (g Grant) Kind() AccessKind {
	return g.kind
}

// Done closes when the credential used for the grant is revoked or expires.
func (g Grant) Done() <-chan struct{} {
	return g.done
}

// Controller owns control-token generations and browser credentials.
type Controller struct {
	mu sync.Mutex

	dataDir string
	options Options

	current  *generation
	previous *generation
	codes    map[[sha256.Size]byte]loginCode
	sessions map[[sha256.Size]byte]*browserSession
}

// Open loads or creates the control token and initializes a credential controller.
func Open(dataDir string, options Options) (*Controller, error) {
	options = resolveOptions(options)
	if options.RotationGrace < 0 {
		return nil, errors.New("auth: rotation grace must not be negative")
	}
	if options.LoginCodeTTL <= 0 {
		return nil, errors.New("auth: login code TTL must be positive")
	}
	if options.SessionTTL <= 0 {
		return nil, errors.New("auth: session TTL must be positive")
	}
	token, err := Ensure(dataDir)
	if err != nil {
		return nil, err
	}
	return &Controller{
		dataDir: dataDir,
		options: options,
		current: newGeneration(token),
		codes:   make(map[[sha256.Size]byte]loginCode),
		sessions: make(
			map[[sha256.Size]byte]*browserSession,
		),
	}, nil
}

func resolveOptions(options Options) Options {
	defaults := DefaultOptions()
	if options.RotationGrace == 0 {
		options.RotationGrace = defaults.RotationGrace
	}
	if options.LoginCodeTTL == 0 {
		options.LoginCodeTTL = defaults.LoginCodeTTL
	}
	if options.SessionTTL == 0 {
		options.SessionTTL = defaults.SessionTTL
	}
	return options
}

// AuthorizeBearer validates one Authorization header against active generations.
func (c *Controller) AuthorizeBearer(authorization string) (Grant, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case Verify(c.current.token, authorization):
		return Grant{kind: BearerAccess, done: c.current.done}, true
	case c.previous != nil && Verify(c.previous.token, authorization):
		return Grant{kind: BearerAccess, done: c.previous.done}, true
	default:
		return Grant{}, false
	}
}

// AuthorizeCookie validates a browser-session cookie.
func (c *Controller) AuthorizeCookie(value string) (Grant, bool) {
	if value == "" {
		return Grant{}, false
	}
	digest := sha256.Sum256([]byte(value))
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()
	session, ok := c.sessions[digest]
	if !ok {
		return Grant{}, false
	}
	if !now.Before(session.expires) {
		session.revoke()
		delete(c.sessions, digest)
		return Grant{}, false
	}
	select {
	case <-session.done:
		delete(c.sessions, digest)
		return Grant{}, false
	default:
		return Grant{kind: CookieAccess, done: session.done}, true
	}
}

// IssueLoginCode creates a short-lived one-time browser login code.
func (c *Controller) IssueLoginCode() (string, error) {
	code, err := randomSessionValue()
	if err != nil {
		return "", fmt.Errorf("auth: generate login code: %w", err)
	}
	digest := sha256.Sum256([]byte(code))

	c.mu.Lock()
	record := loginCode{
		expires:    time.Now().Add(c.options.LoginCodeTTL),
		generation: c.current,
	}
	c.codes[digest] = record
	c.mu.Unlock()

	time.AfterFunc(c.options.LoginCodeTTL, func() {
		c.expireLoginCode(digest, record)
	})
	return code, nil
}

// ExchangeLoginCode consumes a login code and creates a browser session.
func (c *Controller) ExchangeLoginCode(code string) (string, Grant, error) {
	digest := sha256.Sum256([]byte(code))
	now := time.Now()

	c.mu.Lock()
	record, ok := c.codes[digest]
	if ok {
		delete(c.codes, digest)
	}
	if !ok || !now.Before(record.expires) || c.generationRevoked(record.generation) {
		c.mu.Unlock()
		return "", Grant{}, ErrInvalidLoginCode
	}

	value, err := randomSessionValue()
	if err != nil {
		c.mu.Unlock()
		return "", Grant{}, fmt.Errorf("auth: generate browser session: %w", err)
	}
	sessionDigest := sha256.Sum256([]byte(value))
	session := &browserSession{
		expires:    now.Add(c.options.SessionTTL),
		generation: record.generation,
		done:       make(chan struct{}),
	}
	c.sessions[sessionDigest] = session
	c.mu.Unlock()

	time.AfterFunc(c.options.SessionTTL, func() {
		c.expireSession(sessionDigest, session)
	})
	return value, Grant{kind: CookieAccess, done: session.done}, nil
}

// Rotate atomically publishes a new control token and starts the grace period.
func (c *Controller) Rotate() error {
	token, err := newToken()
	if err != nil {
		return fmt.Errorf("auth: generate rotated control token: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := replaceToken(c.dataDir, token); err != nil {
		return err
	}

	if c.previous != nil {
		c.revokeGeneration(c.previous)
	}
	previous := c.current
	c.current = newGeneration(token)
	c.previous = previous
	time.AfterFunc(c.options.RotationGrace, func() {
		c.expireGeneration(previous)
	})
	return nil
}

func (c *Controller) generationRevoked(generation *generation) bool {
	select {
	case <-generation.done:
		return true
	default:
		return false
	}
}

func (c *Controller) expireSession(
	digest [sha256.Size]byte,
	session *browserSession,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions[digest] != session {
		return
	}
	session.revoke()
	delete(c.sessions, digest)
}

func (c *Controller) expireLoginCode(
	digest [sha256.Size]byte,
	record loginCode,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok := c.codes[digest]; ok && current == record {
		delete(c.codes, digest)
	}
}

func (c *Controller) expireGeneration(expired *generation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revokeGeneration(expired)
	if c.previous == expired {
		c.previous = nil
	}
}

func (c *Controller) revokeGeneration(revoked *generation) {
	revoked.revoke()
	for digest, session := range c.sessions {
		if session.generation == revoked {
			session.revoke()
			delete(c.sessions, digest)
		}
	}
	for digest, code := range c.codes {
		if code.generation == revoked {
			delete(c.codes, digest)
		}
	}
}

func randomSessionValue() (string, error) {
	raw := make([]byte, sessionBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func newToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", raw), nil
}

func replaceToken(dataDir, token string) error {
	temp, err := os.CreateTemp(dataDir, "."+TokenFileName+"-*")
	if err != nil {
		return fmt.Errorf("auth: create replacement token: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("auth: secure replacement token: %w", err)
	}
	if _, err := io.WriteString(temp, token); err != nil {
		_ = temp.Close()
		return fmt.Errorf("auth: write replacement token: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("auth: sync replacement token: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("auth: close replacement token: %w", err)
	}
	if err := os.Rename(tempPath, TokenPath(dataDir)); err != nil {
		return fmt.Errorf("auth: publish replacement token: %w", err)
	}
	return nil
}
