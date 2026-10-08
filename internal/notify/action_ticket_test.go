package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/drove/internal/agent"
)

func TestLoadOrCreateActionTicketKey(t *testing.T) {
	dataDir := t.TempDir()
	first, err := LoadOrCreateActionTicketKey(dataDir)
	if err != nil {
		t.Fatalf("create action ticket key: %v", err)
	}
	if len(first) != actionTicketKeyBytes {
		t.Fatalf("key length = %d, want %d", len(first), actionTicketKeyBytes)
	}

	path := filepath.Join(dataDir, "notify", "action-ticket.key")
	assertPathMode(t, path, false, 0o600)
	matches, err := filepath.Glob(filepath.Join(dataDir, "notify", ".action-ticket-*.tmp"))
	if err != nil {
		t.Fatalf("glob temporary action ticket keys: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary action ticket keys remain: %v", matches)
	}

	second, err := LoadOrCreateActionTicketKey(dataDir)
	if err != nil {
		t.Fatalf("reload action ticket key: %v", err)
	}
	if first != second {
		t.Fatal("reload generated a different action ticket key")
	}
}

func TestLoadOrCreateActionTicketKeyConvergesAcrossConcurrentCreators(
	t *testing.T,
) {
	dataDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataDir, "notify"), 0o700); err != nil {
		t.Fatalf("create notification directory: %v", err)
	}

	const creators = 16
	type result struct {
		key ActionTicketKey
		err error
	}
	results := make(chan result, creators)
	var wait sync.WaitGroup
	for range creators {
		wait.Add(1)
		go func() {
			defer wait.Done()
			key, err := LoadOrCreateActionTicketKey(dataDir)
			results <- result{key: key, err: err}
		}()
	}
	wait.Wait()
	close(results)

	var want ActionTicketKey
	for current := range results {
		if current.err != nil {
			t.Fatalf("create action ticket key concurrently: %v", current.err)
		}
		if want == (ActionTicketKey{}) {
			want = current.key
			continue
		}
		if current.key != want {
			t.Fatal("concurrent creators returned different action ticket keys")
		}
	}
}

func TestLoadOrCreateActionTicketKeyRejectsUnsafeFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX permission bits consistently")
	}
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
	}{
		{
			name: "open permissions",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, make([]byte, actionTicketKeyBytes), 0o600); err != nil {
					t.Fatalf("write key: %v", err)
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatalf("set key mode: %v", err)
				}
			},
		},
		{
			name: "wrong length",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
					t.Fatalf("write key: %v", err)
				}
			},
		},
		{
			name: "symlink",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "target.key")
				if err := os.WriteFile(target, make([]byte, actionTicketKeyBytes), 0o600); err != nil {
					t.Fatalf("write target: %v", err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("create symlink: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			dir := filepath.Join(dataDir, "notify")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatalf("create notify directory: %v", err)
			}
			test.prepare(t, filepath.Join(dir, "action-ticket.key"))
			if _, err := LoadOrCreateActionTicketKey(dataDir); err == nil {
				t.Fatal("unsafe action ticket key was accepted")
			}
		})
	}
}

func TestActionTicketsIssueAndConsume(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.October, 8, 14, 0, 0, 0, time.UTC)
	store := openInitializedStore(t, 0)
	saveTestSubscription(t, store, "phone", now.Add(-time.Minute))
	tickets := newTestActionTickets(t, store, now)

	issued, err := tickets.Issue(ctx, ActionTicketRequest{
		AgentID:    agent.ID("agent-1"),
		BlockedSeq: 42,
		Actions: []agent.ActionKind{
			agent.ActionApprove,
			agent.ActionDeny,
			agent.ActionReply,
		},
		DeviceID: "phone",
	})
	if err != nil {
		t.Fatalf("issue action tickets: %v", err)
	}
	if !issued.ExpiresAt.Equal(now.Add(DefaultActionTicketLifetime)) {
		t.Fatalf("expiry = %v, want %v", issued.ExpiresAt, now.Add(DefaultActionTicketLifetime))
	}
	if len(issued.Tickets) != 3 {
		t.Fatalf("ticket count = %d, want 3", len(issued.Tickets))
	}

	for _, issuedTicket := range issued.Tickets {
		claims, err := tickets.Consume(ctx, "agent-1", issuedTicket.Ticket)
		if err != nil {
			t.Fatalf("consume %s ticket: %v", issuedTicket.Action, err)
		}
		if claims.AgentID != "agent-1" ||
			claims.BlockedSeq != 42 ||
			claims.Action != issuedTicket.Action ||
			claims.DeviceID != "phone" ||
			!claims.IssuedAt.Equal(now) ||
			!claims.ExpiresAt.Equal(issued.ExpiresAt) {
			t.Fatalf("claims = %+v", claims)
		}
		if _, err := tickets.Consume(
			ctx,
			"agent-1",
			issuedTicket.Ticket,
		); !errors.Is(err, ErrActionTicketUsed) {
			t.Fatalf("replay error = %v, want ErrActionTicketUsed", err)
		}
	}
}

func TestActionTicketsRejectInvalidBindingsAndRevokedDevices(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.October, 8, 15, 0, 0, 0, time.UTC)
	store := openInitializedStore(t, 0)
	saveTestSubscription(t, store, "phone", now.Add(-time.Minute))
	tickets := newTestActionTickets(t, store, now)

	issued, err := tickets.Issue(ctx, ActionTicketRequest{
		AgentID:    agent.ID("agent-1"),
		BlockedSeq: 42,
		Actions:    []agent.ActionKind{agent.ActionApprove},
		DeviceID:   "phone",
	})
	if err != nil {
		t.Fatalf("issue action ticket: %v", err)
	}
	token := issued.Tickets[0].Ticket

	if _, err := tickets.Consume(
		ctx,
		"agent-2",
		token,
	); !errors.Is(err, ErrActionTicketInvalid) {
		t.Fatalf("wrong-Agent error = %v, want ErrActionTicketInvalid", err)
	}
	signatureStart := strings.LastIndexByte(token, '.') + 1
	replacement := byte('A')
	if token[signatureStart] == replacement {
		replacement = 'B'
	}
	tampered := token[:signatureStart] +
		string(replacement) +
		token[signatureStart+1:]
	if _, err := tickets.Consume(
		ctx,
		"agent-1",
		tampered,
	); !errors.Is(err, ErrActionTicketInvalid) {
		t.Fatalf("tampered error = %v, want ErrActionTicketInvalid", err)
	}
	if revoked, err := store.RevokePushSubscription(
		ctx,
		"phone",
		now.Add(time.Minute),
	); err != nil || !revoked {
		t.Fatalf("revoke device = %t, %v", revoked, err)
	}
	if _, err := tickets.Consume(
		ctx,
		"agent-1",
		token,
	); !errors.Is(err, ErrActionTicketInvalid) {
		t.Fatalf("revoked-device error = %v, want ErrActionTicketInvalid", err)
	}
}

func TestActionTicketsRejectNonCanonicalAndUnsafeClaims(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.October, 8, 15, 30, 0, 0, time.UTC)
	store := openInitializedStore(t, 0)
	saveTestSubscription(t, store, "phone", now.Add(-time.Minute))
	key := ActionTicketKey{1, 2, 3, 4}
	tickets, err := NewActionTickets(store, key, ActionTicketOptions{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create action tickets: %v", err)
	}
	issued, err := tickets.Issue(ctx, ActionTicketRequest{
		AgentID:    agent.ID("agent-1"),
		BlockedSeq: 42,
		Actions:    []agent.ActionKind{agent.ActionApprove},
		DeviceID:   "phone",
	})
	if err != nil {
		t.Fatalf("issue action ticket: %v", err)
	}
	token := issued.Tickets[0].Ticket
	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode issued ticket: %v", err)
	}
	var claims actionTicketClaimsJSON
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("decode issued claims: %v", err)
	}

	unknownFieldPayload := append([]byte(nil), payload[:len(payload)-1]...)
	unknownFieldPayload = append(unknownFieldPayload, []byte(`,"unknown":true}`)...)
	futureClaims := claims
	futureClaims.IssuedAt = now.Add(time.Second).Unix()
	futureClaims.ExpiresAt = now.Add(time.Second + DefaultActionTicketLifetime).Unix()
	excessiveClaims := claims
	excessiveClaims.ExpiresAt = claims.IssuedAt +
		int64(DefaultActionTicketLifetime/time.Second) + 1

	signatureStart := strings.LastIndexByte(token, '.') + 1
	replacement := byte('A')
	if token[signatureStart] == replacement {
		replacement = 'B'
	}
	badMAC := token[:signatureStart] +
		string(replacement) +
		token[signatureStart+1:]

	tests := []struct {
		name  string
		token string
	}{
		{
			name:  "padded Base64URL",
			token: signEncodedActionTicketForTest(key, parts[1]+"="),
		},
		{
			name:  "noncanonical JSON",
			token: signActionTicketPayloadForTest(key, append([]byte{' '}, payload...)),
		},
		{
			name:  "unknown claim",
			token: signActionTicketPayloadForTest(key, unknownFieldPayload),
		},
		{name: "bad MAC", token: badMAC},
		{name: "future issue time", token: signActionTicketClaimsForTest(t, key, futureClaims)},
		{
			name:  "excessive lifetime",
			token: signActionTicketClaimsForTest(t, key, excessiveClaims),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := tickets.Consume(
				ctx,
				"agent-1",
				test.token,
			); !errors.Is(err, ErrActionTicketInvalid) {
				t.Fatalf("consume error = %v, want ErrActionTicketInvalid", err)
			}
		})
	}
}

func TestActionTicketsRejectExpiryWithoutConsuming(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.October, 8, 15, 45, 0, 0, time.UTC)
	store := openInitializedStore(t, 0)
	saveTestSubscription(t, store, "phone", now.Add(-time.Minute))
	tickets, err := NewActionTickets(
		store,
		ActionTicketKey{1, 2, 3, 4},
		ActionTicketOptions{Now: func() time.Time { return now }},
	)
	if err != nil {
		t.Fatalf("create action tickets: %v", err)
	}
	issued, err := tickets.Issue(ctx, ActionTicketRequest{
		AgentID:    agent.ID("agent-1"),
		BlockedSeq: 42,
		Actions:    []agent.ActionKind{agent.ActionApprove},
		DeviceID:   "phone",
	})
	if err != nil {
		t.Fatalf("issue action ticket: %v", err)
	}

	now = issued.ExpiresAt
	if _, err := tickets.Consume(
		ctx,
		"agent-1",
		issued.Tickets[0].Ticket,
	); !errors.Is(err, ErrActionTicketExpired) {
		t.Fatalf("expiry error = %v, want ErrActionTicketExpired", err)
	}
	var consumed int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM action_tickets WHERE consumed_at IS NOT NULL`,
	).Scan(&consumed); err != nil {
		t.Fatalf("count consumed tickets: %v", err)
	}
	if consumed != 0 {
		t.Fatalf("expired ticket consumed rows = %d, want 0", consumed)
	}
}

func TestActionTicketRowsStoreOnlyDigestAndBoundedMetadata(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.October, 8, 15, 50, 0, 0, time.UTC)
	store := openInitializedStore(t, 0)
	saveTestSubscription(t, store, "phone", now.Add(-time.Minute))
	tickets := newTestActionTickets(t, store, now)
	issued, err := tickets.Issue(ctx, ActionTicketRequest{
		AgentID:    agent.ID("agent-1"),
		BlockedSeq: 42,
		Actions:    []agent.ActionKind{agent.ActionReply},
		DeviceID:   "phone",
	})
	if err != nil {
		t.Fatalf("issue action ticket: %v", err)
	}
	token := issued.Tickets[0].Ticket
	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode ticket payload: %v", err)
	}
	var claims actionTicketClaimsJSON
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("decode ticket claims: %v", err)
	}
	rawJTI, err := base64.RawURLEncoding.DecodeString(claims.JTI)
	if err != nil {
		t.Fatalf("decode ticket JTI: %v", err)
	}

	rows, err := store.db.Query(`PRAGMA table_info(action_tickets)`)
	if err != nil {
		t.Fatalf("read action ticket schema: %v", err)
	}
	var columns []string
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			primaryKey   int
		)
		if err := rows.Scan(
			&cid,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			rows.Close()
			t.Fatalf("scan action ticket schema: %v", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close action ticket schema rows: %v", err)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate action ticket schema: %v", err)
	}
	wantColumns := []string{
		"jti_digest",
		"agent_id",
		"blocked_seq",
		"action",
		"device_id",
		"issued_at",
		"expires_at",
		"consumed_at",
	}
	if !reflect.DeepEqual(columns, wantColumns) {
		t.Fatalf("action ticket columns = %v, want %v", columns, wantColumns)
	}

	var (
		digest       []byte
		storedAgent  string
		storedSeq    string
		storedAction string
		storedDevice string
		issuedAt     string
		expiresAt    string
	)
	if err := store.db.QueryRow(
		`SELECT jti_digest, agent_id, blocked_seq, action, device_id,
		        issued_at, expires_at
		 FROM action_tickets`,
	).Scan(
		&digest,
		&storedAgent,
		&storedSeq,
		&storedAction,
		&storedDevice,
		&issuedAt,
		&expiresAt,
	); err != nil {
		t.Fatalf("read action ticket row: %v", err)
	}
	wantDigest := sha256.Sum256(rawJTI)
	if !bytes.Equal(digest, wantDigest[:]) || bytes.Equal(digest, rawJTI) {
		t.Fatal("action ticket row did not store only the JTI digest")
	}
	if storedAgent != claims.AgentID ||
		storedSeq != claims.BlockedSeq ||
		storedAction != claims.Action ||
		storedDevice != claims.DeviceID ||
		issuedAt != formatTime(time.Unix(claims.IssuedAt, 0)) ||
		expiresAt != formatTime(time.Unix(claims.ExpiresAt, 0)) {
		t.Fatalf(
			"stored metadata = %q/%q/%q/%q/%q/%q",
			storedAgent,
			storedSeq,
			storedAction,
			storedDevice,
			issuedAt,
			expiresAt,
		)
	}
	storedText := strings.Join(
		[]string{
			storedAgent,
			storedSeq,
			storedAction,
			storedDevice,
			issuedAt,
			expiresAt,
		},
		"\n",
	)
	if strings.Contains(storedText, token) || strings.Contains(storedText, claims.JTI) {
		t.Fatal("action ticket row contains a raw ticket or JTI")
	}
}

func TestActionTicketsConsumeOnceAcrossConcurrencyAndRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.October, 8, 16, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "notify.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open notification database: %v", err)
	}
	saveTestSubscription(t, store, "phone", now.Add(-time.Minute))
	key := ActionTicketKey{1, 2, 3, 4}
	tickets, err := NewActionTickets(store, key, ActionTicketOptions{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create action tickets: %v", err)
	}
	issued, err := tickets.Issue(ctx, ActionTicketRequest{
		AgentID:    agent.ID("agent-1"),
		BlockedSeq: 42,
		Actions:    []agent.ActionKind{agent.ActionDeny},
		DeviceID:   "phone",
	})
	if err != nil {
		t.Fatalf("issue action ticket: %v", err)
	}
	token := issued.Tickets[0].Ticket

	const consumers = 8
	results := make(chan error, consumers)
	var wait sync.WaitGroup
	for range consumers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, consumeErr := tickets.Consume(ctx, "agent-1", token)
			results <- consumeErr
		}()
	}
	wait.Wait()
	close(results)

	successes := 0
	used := 0
	for consumeErr := range results {
		switch {
		case consumeErr == nil:
			successes++
		case errors.Is(consumeErr, ErrActionTicketUsed):
			used++
		default:
			t.Fatalf("consume error = %v", consumeErr)
		}
	}
	if successes != 1 || used != consumers-1 {
		t.Fatalf("consume results = %d success, %d used", successes, used)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close notification database: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen notification database: %v", err)
	}
	defer reopened.Close()
	restarted, err := NewActionTickets(reopened, key, ActionTicketOptions{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("recreate action tickets: %v", err)
	}
	if _, err := restarted.Consume(
		ctx,
		"agent-1",
		token,
	); !errors.Is(err, ErrActionTicketUsed) {
		t.Fatalf("restart replay error = %v, want ErrActionTicketUsed", err)
	}
}

func newTestActionTickets(
	t *testing.T,
	store *Store,
	now time.Time,
) *ActionTickets {
	t.Helper()
	key := ActionTicketKey{1, 2, 3, 4}
	tickets, err := NewActionTickets(store, key, ActionTicketOptions{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create action tickets: %v", err)
	}
	return tickets
}

func signActionTicketClaimsForTest(
	t *testing.T,
	key ActionTicketKey,
	claims actionTicketClaimsJSON,
) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("encode action ticket claims: %v", err)
	}
	return signActionTicketPayloadForTest(key, payload)
}

func signActionTicketPayloadForTest(
	key ActionTicketKey,
	payload []byte,
) string {
	return signEncodedActionTicketForTest(
		key,
		base64.RawURLEncoding.EncodeToString(payload),
	)
}

func signEncodedActionTicketForTest(
	key ActionTicketKey,
	encodedPayload string,
) string {
	signed := actionTicketVersion + "." + encodedPayload
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(signed))
	return signed + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
