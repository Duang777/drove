package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Duang777/drove/internal/agent"
)

const (
	actionTicketKeyBytes = 32
	actionTicketVersion  = "v1"
	maxActionTicketBytes = 4096
	maxTicketIdentity    = 256

	// DefaultActionTicketLifetime limits the replay window for remote actions.
	DefaultActionTicketLifetime = 10 * time.Minute
)

var (
	// ErrActionTicketsUnavailable means the action-ticket subsystem is disabled.
	ErrActionTicketsUnavailable = errors.New("notify: action tickets are unavailable")
	// ErrActionTicketInvalid means a ticket is malformed, forged, or misbound.
	ErrActionTicketInvalid = errors.New("notify: action ticket is invalid")
	// ErrActionTicketExpired means a valid ticket is outside its lifetime.
	ErrActionTicketExpired = errors.New("notify: action ticket expired")
	// ErrActionTicketUsed means a valid ticket was already consumed.
	ErrActionTicketUsed = errors.New("notify: action ticket was already used")
)

// ActionTicketKey is an independent 256-bit HMAC key.
type ActionTicketKey [actionTicketKeyBytes]byte

// ActionTicketOptions configures ticket lifetime and testable dependencies.
type ActionTicketOptions struct {
	Lifetime time.Duration
	Now      func() time.Time
	Random   io.Reader
}

// ActionTicketRequest describes tickets for one actionable Blocked occurrence.
type ActionTicketRequest struct {
	AgentID    agent.ID
	BlockedSeq uint64
	Actions    []agent.ActionKind
	DeviceID   string
}

// IssuedActionTicket binds one opaque token to one explicit action.
type IssuedActionTicket struct {
	Action agent.ActionKind `json:"action"`
	Ticket string           `json:"ticket"`
}

// IssuedActionTickets contains one ticket per available action.
type IssuedActionTickets struct {
	Tickets   []IssuedActionTicket `json:"tickets"`
	ExpiresAt time.Time            `json:"expires_at"`
}

// ActionTicketClaims are the verified bindings recovered during consumption.
type ActionTicketClaims struct {
	AgentID    agent.ID
	BlockedSeq uint64
	Action     agent.ActionKind
	DeviceID   string
	IssuedAt   time.Time
	ExpiresAt  time.Time
}

// ActionTickets signs tickets and owns their durable replay state.
type ActionTickets struct {
	store    *Store
	key      ActionTicketKey
	lifetime time.Duration
	now      func() time.Time
	random   io.Reader
}

type actionTicketClaimsJSON struct {
	Action     string `json:"action"`
	AgentID    string `json:"agent_id"`
	BlockedSeq string `json:"blocked_seq"`
	DeviceID   string `json:"device_id"`
	ExpiresAt  int64  `json:"exp"`
	IssuedAt   int64  `json:"iat"`
	JTI        string `json:"jti"`
}

// LoadOrCreateActionTicketKey loads or atomically creates the ticket HMAC key.
func LoadOrCreateActionTicketKey(dataDir string) (ActionTicketKey, error) {
	dir := filepath.Join(dataDir, "notify")
	if err := ensurePrivateDirectory(dir); err != nil {
		return ActionTicketKey{}, err
	}
	path := filepath.Join(dir, "action-ticket.key")
	key, found, err := loadActionTicketKey(path)
	if err != nil {
		return ActionTicketKey{}, err
	}
	if found {
		return key, nil
	}
	if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
		return ActionTicketKey{}, fmt.Errorf("notify: generate action ticket key: %w", err)
	}
	if err := writeActionTicketKeyAtomically(path, key); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, existingFound, loadErr := loadActionTicketKey(path)
			if loadErr != nil {
				return ActionTicketKey{}, loadErr
			}
			if existingFound {
				return existing, nil
			}
		}
		return ActionTicketKey{}, err
	}
	return key, nil
}

func loadActionTicketKey(path string) (ActionTicketKey, bool, error) {
	var key ActionTicketKey
	info, err := os.Lstat(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		return key, false, nil
	default:
		return key, false, fmt.Errorf(
			"notify: inspect action ticket key %q: %w",
			path,
			err,
		)
	}
	if !info.Mode().IsRegular() {
		return key, false, fmt.Errorf(
			"notify: action ticket key %q must be a regular file",
			path,
		)
	}
	if info.Mode().Perm() != 0o600 {
		return key, false, fmt.Errorf(
			"notify: action ticket key %q permissions are %04o, want 0600",
			path,
			info.Mode().Perm(),
		)
	}
	if info.Size() != actionTicketKeyBytes {
		return key, false, fmt.Errorf(
			"notify: action ticket key %q has %d bytes, want %d",
			path,
			info.Size(),
			actionTicketKeyBytes,
		)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return key, false, fmt.Errorf(
			"notify: read action ticket key %q: %w",
			path,
			err,
		)
	}
	copy(key[:], raw)
	return key, true, nil
}

func writeActionTicketKeyAtomically(
	path string,
	key ActionTicketKey,
) (err error) {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".action-ticket-*.tmp")
	if err != nil {
		return fmt.Errorf("notify: create temporary action ticket key: %w", err)
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
				fmt.Errorf("notify: remove temporary action ticket key: %w", removeErr),
			)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("notify: secure temporary action ticket key: %w", err)
	}
	if _, err := file.Write(key[:]); err != nil {
		return fmt.Errorf("notify: write action ticket key: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("notify: sync action ticket key: %w", err)
	}
	if err := file.Close(); err != nil {
		file = nil
		return fmt.Errorf("notify: close action ticket key: %w", err)
	}
	file = nil
	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return fmt.Errorf("notify: install action ticket key: %w", err)
	}
	return nil
}

// NewActionTickets validates dependencies and constructs a ticket manager.
func NewActionTickets(
	store *Store,
	key ActionTicketKey,
	options ActionTicketOptions,
) (*ActionTickets, error) {
	if store == nil {
		return nil, errors.New("notify: action ticket store is required")
	}
	if options.Lifetime == 0 {
		options.Lifetime = DefaultActionTicketLifetime
	}
	if options.Lifetime < time.Second ||
		options.Lifetime > DefaultActionTicketLifetime ||
		options.Lifetime%time.Second != 0 {
		return nil, errors.New(
			"notify: action ticket lifetime must be whole seconds between 1s and 10m",
		)
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &ActionTickets{
		store:    store,
		key:      key,
		lifetime: options.Lifetime,
		now:      options.Now,
		random:   options.Random,
	}, nil
}

// Issue creates one independently consumable ticket for each requested action.
func (t *ActionTickets) Issue(
	ctx context.Context,
	request ActionTicketRequest,
) (IssuedActionTickets, error) {
	if err := validateActionTicketRequest(request); err != nil {
		return IssuedActionTickets{}, err
	}
	now := t.now().UTC().Truncate(time.Second)
	expiresAt := now.Add(t.lifetime)

	tx, err := t.store.db.BeginTx(ctx, nil)
	if err != nil {
		return IssuedActionTickets{}, fmt.Errorf(
			"notify: begin action ticket issue: %w",
			err,
		)
	}
	defer tx.Rollback()
	if err := requireActivePushSubscription(ctx, tx, request.DeviceID); err != nil {
		return IssuedActionTickets{}, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM action_tickets WHERE expires_at <= ?`,
		formatTime(now),
	); err != nil {
		return IssuedActionTickets{}, fmt.Errorf(
			"notify: remove expired action tickets: %w",
			err,
		)
	}

	result := IssuedActionTickets{
		Tickets:   make([]IssuedActionTicket, 0, len(request.Actions)),
		ExpiresAt: expiresAt,
	}
	for _, action := range request.Actions {
		claims, token, digest, err := t.newTicket(request, action, now, expiresAt)
		if err != nil {
			return IssuedActionTickets{}, err
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO action_tickets (
				jti_digest, agent_id, blocked_seq, action, device_id,
				issued_at, expires_at, consumed_at
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
			digest[:],
			claims.AgentID,
			claims.BlockedSeq,
			claims.Action,
			claims.DeviceID,
			formatTime(time.Unix(claims.IssuedAt, 0)),
			formatTime(time.Unix(claims.ExpiresAt, 0)),
		); err != nil {
			return IssuedActionTickets{}, fmt.Errorf(
				"notify: persist %s action ticket: %w",
				action,
				err,
			)
		}
		result.Tickets = append(result.Tickets, IssuedActionTicket{
			Action: action,
			Ticket: token,
		})
	}
	if err := tx.Commit(); err != nil {
		return IssuedActionTickets{}, fmt.Errorf(
			"notify: commit action ticket issue: %w",
			err,
		)
	}
	return result, nil
}

func (t *ActionTickets) newTicket(
	request ActionTicketRequest,
	action agent.ActionKind,
	issuedAt time.Time,
	expiresAt time.Time,
) (actionTicketClaimsJSON, string, [sha256.Size]byte, error) {
	var jtiBytes [actionTicketKeyBytes]byte
	if _, err := io.ReadFull(t.random, jtiBytes[:]); err != nil {
		return actionTicketClaimsJSON{}, "", [sha256.Size]byte{}, fmt.Errorf(
			"notify: generate action ticket identifier: %w",
			err,
		)
	}
	claims := actionTicketClaimsJSON{
		Action:     string(action),
		AgentID:    string(request.AgentID),
		BlockedSeq: strconv.FormatUint(request.BlockedSeq, 10),
		DeviceID:   request.DeviceID,
		ExpiresAt:  expiresAt.Unix(),
		IssuedAt:   issuedAt.Unix(),
		JTI:        base64.RawURLEncoding.EncodeToString(jtiBytes[:]),
	}
	token, err := t.sign(claims)
	if err != nil {
		return actionTicketClaimsJSON{}, "", [sha256.Size]byte{}, err
	}
	return claims, token, sha256.Sum256(jtiBytes[:]), nil
}

// Consume verifies all bindings and durably marks one ticket used.
func (t *ActionTickets) Consume(
	ctx context.Context,
	expectedAgent agent.ID,
	token string,
) (ActionTicketClaims, error) {
	now := t.now().UTC().Truncate(time.Second)
	claims, digest, err := t.verify(token, now)
	if err != nil {
		return ActionTicketClaims{}, err
	}
	if claims.AgentID != string(expectedAgent) {
		return ActionTicketClaims{}, ErrActionTicketInvalid
	}

	tx, err := t.store.db.BeginTx(ctx, nil)
	if err != nil {
		return ActionTicketClaims{}, fmt.Errorf(
			"notify: begin action ticket consumption: %w",
			err,
		)
	}
	defer tx.Rollback()

	var (
		storedAgent      string
		storedBlockedSeq string
		storedAction     string
		storedDevice     string
		storedIssuedAt   string
		storedExpiresAt  string
		consumedAt       sql.NullString
	)
	err = tx.QueryRowContext(
		ctx,
		`SELECT agent_id, blocked_seq, action, device_id,
		        issued_at, expires_at, consumed_at
		 FROM action_tickets
		 WHERE jti_digest = ?`,
		digest[:],
	).Scan(
		&storedAgent,
		&storedBlockedSeq,
		&storedAction,
		&storedDevice,
		&storedIssuedAt,
		&storedExpiresAt,
		&consumedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ActionTicketClaims{}, ErrActionTicketInvalid
	}
	if err != nil {
		return ActionTicketClaims{}, fmt.Errorf(
			"notify: read action ticket: %w",
			err,
		)
	}
	if storedAgent != claims.AgentID ||
		storedBlockedSeq != claims.BlockedSeq ||
		storedAction != claims.Action ||
		storedDevice != claims.DeviceID ||
		storedIssuedAt != formatTime(time.Unix(claims.IssuedAt, 0)) ||
		storedExpiresAt != formatTime(time.Unix(claims.ExpiresAt, 0)) {
		return ActionTicketClaims{}, ErrActionTicketInvalid
	}
	if consumedAt.Valid {
		return ActionTicketClaims{}, ErrActionTicketUsed
	}
	if err := requireActivePushSubscription(ctx, tx, claims.DeviceID); err != nil {
		return ActionTicketClaims{}, ErrActionTicketInvalid
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE action_tickets
		 SET consumed_at = ?
		 WHERE jti_digest = ? AND consumed_at IS NULL`,
		formatTime(now),
		digest[:],
	)
	if err != nil {
		return ActionTicketClaims{}, fmt.Errorf(
			"notify: consume action ticket: %w",
			err,
		)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ActionTicketClaims{}, fmt.Errorf(
			"notify: count consumed action tickets: %w",
			err,
		)
	}
	if changed != 1 {
		return ActionTicketClaims{}, ErrActionTicketUsed
	}
	if err := tx.Commit(); err != nil {
		return ActionTicketClaims{}, fmt.Errorf(
			"notify: commit action ticket consumption: %w",
			err,
		)
	}

	blockedSeq, _ := strconv.ParseUint(claims.BlockedSeq, 10, 64)
	return ActionTicketClaims{
		AgentID:    agent.ID(claims.AgentID),
		BlockedSeq: blockedSeq,
		Action:     agent.ActionKind(claims.Action),
		DeviceID:   claims.DeviceID,
		IssuedAt:   time.Unix(claims.IssuedAt, 0).UTC(),
		ExpiresAt:  time.Unix(claims.ExpiresAt, 0).UTC(),
	}, nil
}

func (t *ActionTickets) sign(claims actionTicketClaimsJSON) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("notify: encode action ticket claims: %w", err)
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signed := actionTicketVersion + "." + encodedPayload
	mac := hmac.New(sha256.New, t.key[:])
	_, _ = mac.Write([]byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (t *ActionTickets) verify(
	token string,
	now time.Time,
) (actionTicketClaimsJSON, [sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if token == "" || len(token) > maxActionTicketBytes {
		return actionTicketClaimsJSON{}, digest, ErrActionTicketInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != actionTicketVersion {
		return actionTicketClaimsJSON{}, digest, ErrActionTicketInvalid
	}
	payload, err := decodeCanonicalBase64URL(parts[1])
	if err != nil {
		return actionTicketClaimsJSON{}, digest, ErrActionTicketInvalid
	}
	signature, err := decodeCanonicalBase64URL(parts[2])
	if err != nil || len(signature) != sha256.Size {
		return actionTicketClaimsJSON{}, digest, ErrActionTicketInvalid
	}
	mac := hmac.New(sha256.New, t.key[:])
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return actionTicketClaimsJSON{}, digest, ErrActionTicketInvalid
	}

	var claims actionTicketClaimsJSON
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return actionTicketClaimsJSON{}, digest, ErrActionTicketInvalid
	}
	if err := requireJSONEOF(decoder); err != nil {
		return actionTicketClaimsJSON{}, digest, ErrActionTicketInvalid
	}
	canonical, err := json.Marshal(claims)
	if err != nil || !bytes.Equal(payload, canonical) {
		return actionTicketClaimsJSON{}, digest, ErrActionTicketInvalid
	}
	jti, err := validateActionTicketClaims(claims, now)
	if err != nil {
		return actionTicketClaimsJSON{}, digest, err
	}
	return claims, sha256.Sum256(jti), nil
}

func validateActionTicketRequest(request ActionTicketRequest) error {
	if err := validateTicketIdentity("Agent ID", string(request.AgentID)); err != nil {
		return err
	}
	if request.BlockedSeq == 0 {
		return errors.New("notify: action ticket Blocked sequence is required")
	}
	if err := validateTicketIdentity("device ID", request.DeviceID); err != nil {
		return err
	}
	if len(request.Actions) == 0 || len(request.Actions) > 3 {
		return errors.New("notify: action ticket actions must contain 1-3 entries")
	}
	seen := make(map[agent.ActionKind]struct{}, len(request.Actions))
	for _, action := range request.Actions {
		if !agent.ValidActionKind(action) {
			return fmt.Errorf("notify: invalid action ticket action %q", action)
		}
		if _, duplicate := seen[action]; duplicate {
			return fmt.Errorf("notify: duplicate action ticket action %q", action)
		}
		seen[action] = struct{}{}
	}
	return nil
}

func validateActionTicketClaims(
	claims actionTicketClaimsJSON,
	now time.Time,
) ([]byte, error) {
	if err := validateTicketIdentity("Agent ID", claims.AgentID); err != nil {
		return nil, ErrActionTicketInvalid
	}
	if err := validateTicketIdentity("device ID", claims.DeviceID); err != nil {
		return nil, ErrActionTicketInvalid
	}
	if !agent.ValidActionKind(agent.ActionKind(claims.Action)) {
		return nil, ErrActionTicketInvalid
	}
	blockedSeq, err := strconv.ParseUint(claims.BlockedSeq, 10, 64)
	if err != nil ||
		blockedSeq == 0 ||
		strconv.FormatUint(blockedSeq, 10) != claims.BlockedSeq {
		return nil, ErrActionTicketInvalid
	}
	jti, err := decodeCanonicalBase64URL(claims.JTI)
	if err != nil || len(jti) != actionTicketKeyBytes {
		return nil, ErrActionTicketInvalid
	}
	maxLifetimeSeconds := int64(DefaultActionTicketLifetime / time.Second)
	if claims.IssuedAt <= 0 ||
		claims.ExpiresAt <= claims.IssuedAt ||
		claims.ExpiresAt-claims.IssuedAt > maxLifetimeSeconds ||
		claims.IssuedAt > now.Unix() {
		return nil, ErrActionTicketInvalid
	}
	if now.Unix() >= claims.ExpiresAt {
		return nil, ErrActionTicketExpired
	}
	return jti, nil
}

func validateTicketIdentity(name string, value string) error {
	if value == "" ||
		len(value) > maxTicketIdentity ||
		!utf8.ValidString(value) ||
		value != strings.TrimSpace(value) {
		return fmt.Errorf("notify: action ticket %s is invalid", name)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("notify: action ticket %s is invalid", name)
		}
	}
	return nil
}

func decodeCanonicalBase64URL(raw string) ([]byte, error) {
	if raw == "" || strings.Contains(raw, "=") {
		return nil, ErrActionTicketInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil ||
		base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return nil, ErrActionTicketInvalid
	}
	return decoded, nil
}

func requireActivePushSubscription(
	ctx context.Context,
	tx *sql.Tx,
	deviceID string,
) error {
	var revokedAt sql.NullString
	err := tx.QueryRowContext(
		ctx,
		`SELECT revoked_at FROM push_subscriptions WHERE id = ?`,
		deviceID,
	).Scan(&revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPushSubscriptionNotFound
	}
	if err != nil {
		return fmt.Errorf("notify: read action ticket device: %w", err)
	}
	if revokedAt.Valid {
		return ErrPushSubscriptionRevoked
	}
	return nil
}
