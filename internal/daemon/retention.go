package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Duang777/drove/internal/store"
)

const outputRetentionInterval = 24 * time.Hour

type outputRetentionPruner func(
	context.Context,
	*store.Store,
	time.Time,
) (int64, error)

func (d *Daemon) pruneRetainedOutput(
	ctx context.Context,
	st *store.Store,
	now time.Time,
) (int64, error) {
	days := d.cfg.Storage.OutputRetentionDays
	if days == 0 {
		return 0, nil
	}
	cutoff := now.UTC().AddDate(0, 0, -days)
	deleted, err := d.retentionPruner()(ctx, st, cutoff)
	if err != nil {
		return deleted, fmt.Errorf("prune output before %s: %w", cutoff.Format(time.RFC3339), err)
	}
	return deleted, nil
}

func (d *Daemon) startRetentionLoop(
	st *store.Store,
	log *slog.Logger,
) (context.CancelFunc, <-chan struct{}) {
	done := make(chan struct{})
	if d.cfg.Storage.OutputRetentionDays == 0 {
		close(done)
		return func() {}, done
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(done)
		var ticker *time.Ticker
		ticks := d.retentionTicks
		if ticks == nil {
			ticker = time.NewTicker(outputRetentionInterval)
			ticks = ticker.C
			defer ticker.Stop()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case tick, ok := <-ticks:
				if !ok {
					return
				}
				deleted, err := d.pruneRetainedOutput(ctx, st, tick)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					log.Warn("scheduled output retention failed", "err", err)
					continue
				}
				log.Info("scheduled output retention completed", "deleted", deleted)
			}
		}
	}()
	return cancel, done
}

func (d *Daemon) retentionNow() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now().UTC()
}

func (d *Daemon) retentionPruner() outputRetentionPruner {
	if d.pruneOutput != nil {
		return d.pruneOutput
	}
	return func(ctx context.Context, st *store.Store, cutoff time.Time) (int64, error) {
		return st.PruneOutputAttachments(ctx, cutoff)
	}
}
