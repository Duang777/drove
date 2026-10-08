package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	deliveryPending   = "pending"
	deliveryLeased    = "leased"
	deliveryDelivered = "delivered"
	deliveryPermanent = "permanent"
	deliveryObsolete  = "obsolete"
)

func (s *Store) claimDelivery(
	ctx context.Context,
	kind ChannelKind,
	now time.Time,
	leaseDuration time.Duration,
) (Delivery, bool, error) {
	if !validChannelKind(kind) {
		return Delivery{}, false, fmt.Errorf("notify: invalid claim channel %q", kind)
	}
	if now.IsZero() {
		return Delivery{}, false, errors.New("notify: claim time is required")
	}
	if leaseDuration <= 0 {
		return Delivery{}, false, errors.New("notify: lease duration must be positive")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Delivery{}, false, fmt.Errorf("notify: begin delivery claim: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE notify_deliveries
		 SET state = 'pending', lease_until = NULL
		 WHERE channel = ? AND state = 'leased' AND lease_until <= ?`,
		kind,
		formatTime(now),
	); err != nil {
		return Delivery{}, false, fmt.Errorf("notify: recover expired delivery leases: %w", err)
	}

	var delivery Delivery
	var payload string
	err = tx.QueryRowContext(
		ctx,
		`SELECT id, source_seq, agent_id, channel, target_id, payload, attempts
		 FROM notify_deliveries
		 WHERE channel = ? AND state = 'pending' AND not_before <= ?
		 ORDER BY not_before, source_seq, id
		 LIMIT 1`,
		kind,
		formatTime(now),
	).Scan(
		&delivery.ID,
		&delivery.SourceSeq,
		&delivery.AgentID,
		&delivery.Channel,
		&delivery.TargetID,
		&payload,
		&delivery.Attempts,
	)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return Delivery{}, false, fmt.Errorf(
				"notify: commit empty delivery claim: %w",
				err,
			)
		}
		return Delivery{}, false, nil
	}
	if err != nil {
		return Delivery{}, false, fmt.Errorf("notify: select delivery: %w", err)
	}
	if err := json.Unmarshal([]byte(payload), &delivery.Notification); err != nil {
		return Delivery{}, false, fmt.Errorf(
			"notify: decode delivery %q payload: %w",
			delivery.ID,
			err,
		)
	}
	if err := validateNotification(delivery.Notification, delivery); err != nil {
		return Delivery{}, false, err
	}

	if kind == ChannelWebPush {
		subscription, found, err := pushSubscriptionInTx(
			ctx,
			tx,
			delivery.TargetID,
		)
		if err != nil {
			return Delivery{}, false, err
		}
		if !found || subscription.RevokedAt != nil {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE notify_deliveries
				 SET state = 'obsolete', lease_until = NULL
				 WHERE id = ? AND state = 'pending'`,
				delivery.ID,
			); err != nil {
				return Delivery{}, false, fmt.Errorf(
					"notify: obsolete missing push target delivery: %w",
					err,
				)
			}
			if err := tx.Commit(); err != nil {
				return Delivery{}, false, fmt.Errorf(
					"notify: commit missing push target delivery: %w",
					err,
				)
			}
			return Delivery{}, false, nil
		}
		delivery.Subscription = &subscription
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE notify_deliveries
		 SET state = 'leased', attempts = attempts + 1, lease_until = ?
		 WHERE id = ? AND state = 'pending'`,
		formatTime(now.Add(leaseDuration)),
		delivery.ID,
	)
	if err != nil {
		return Delivery{}, false, fmt.Errorf("notify: lease delivery: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Delivery{}, false, fmt.Errorf("notify: count leased delivery: %w", err)
	}
	if changed != 1 {
		return Delivery{}, false, errors.New("notify: delivery changed during claim")
	}
	if err := tx.Commit(); err != nil {
		return Delivery{}, false, fmt.Errorf("notify: commit delivery claim: %w", err)
	}
	delivery.Attempts++
	return delivery, true, nil
}

func pushSubscriptionInTx(
	ctx context.Context,
	tx *sql.Tx,
	id string,
) (PushSubscription, bool, error) {
	subscription, err := scanPushSubscription(tx.QueryRowContext(
		ctx,
		`SELECT id, endpoint, p256dh, auth, device_name, created_at, revoked_at
		 FROM push_subscriptions
		 WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return PushSubscription{}, false, nil
	}
	if err != nil {
		return PushSubscription{}, false, err
	}
	return subscription, true, nil
}

func validateNotification(notification Notification, delivery Delivery) error {
	if notification.ID == "" ||
		notification.AgentID == "" ||
		notification.AgentName == "" ||
		notification.Vendor == "" ||
		notification.State == "" ||
		notification.BlockedSeq == 0 ||
		notification.OccurredAt.IsZero() ||
		notification.DeepLink == "" {
		return fmt.Errorf(
			"notify: delivery %q contains incomplete notification metadata",
			delivery.ID,
		)
	}
	if notification.AgentID != delivery.AgentID ||
		notification.BlockedSeq != delivery.SourceSeq {
		return fmt.Errorf(
			"notify: delivery %q payload does not match its envelope",
			delivery.ID,
		)
	}
	return nil
}

func (s *Store) markDelivered(
	ctx context.Context,
	id string,
	now time.Time,
) (bool, error) {
	if now.IsZero() {
		return false, errors.New("notify: delivery completion time is required")
	}
	return s.transitionDelivery(
		ctx,
		id,
		`UPDATE notify_deliveries
		 SET state = 'delivered', delivered_at = ?, lease_until = NULL,
		     last_error_code = ''
		 WHERE id = ? AND state = 'leased'`,
		formatTime(now),
		id,
	)
}

func (s *Store) markRetry(
	ctx context.Context,
	id string,
	notBefore time.Time,
	code string,
) (bool, error) {
	if notBefore.IsZero() {
		return false, errors.New("notify: delivery retry time is required")
	}
	if len(code) > 64 {
		return false, errors.New("notify: delivery error code exceeds 64 bytes")
	}
	return s.transitionDelivery(
		ctx,
		id,
		`UPDATE notify_deliveries
		 SET state = 'pending', not_before = ?, lease_until = NULL,
		     last_error_code = ?
		 WHERE id = ? AND state = 'leased'`,
		formatTime(notBefore),
		code,
		id,
	)
}

func (s *Store) markPermanent(
	ctx context.Context,
	id string,
	code string,
) (bool, error) {
	if len(code) > 64 {
		return false, errors.New("notify: delivery error code exceeds 64 bytes")
	}
	return s.transitionDelivery(
		ctx,
		id,
		`UPDATE notify_deliveries
		 SET state = 'permanent', lease_until = NULL, last_error_code = ?
		 WHERE id = ? AND state = 'leased'`,
		code,
		id,
	)
}

func (s *Store) transitionDelivery(
	ctx context.Context,
	id string,
	query string,
	args ...any,
) (bool, error) {
	if id == "" {
		return false, errors.New("notify: delivery ID is required")
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("notify: update delivery %q: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("notify: count delivery %q update: %w", id, err)
	}
	return changed == 1, nil
}

func (s *Store) releaseLeases(ctx context.Context) error {
	if _, err := s.db.ExecContext(
		ctx,
		`UPDATE notify_deliveries
		 SET state = 'pending', lease_until = NULL
		 WHERE state = 'leased'`,
	); err != nil {
		return fmt.Errorf("notify: release delivery leases: %w", err)
	}
	return nil
}

func (s *Store) deliveryCounts(
	ctx context.Context,
) (map[string]int, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT state, COUNT(*) FROM notify_deliveries GROUP BY state`,
	)
	if err != nil {
		return nil, fmt.Errorf("notify: count deliveries: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, fmt.Errorf("notify: scan delivery count: %w", err)
		}
		counts[state] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notify: iterate delivery counts: %w", err)
	}
	return counts, nil
}
