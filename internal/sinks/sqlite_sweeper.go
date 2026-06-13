package sinks

import (
	"context"
	"log/slog"
	"time"
)

const defaultSweeperInterval = time.Minute

// SweeperPolicy carries the retention bounds for one sweep run.
// Exactly one of MaxAge and MaxCount should be non-zero; the caller
// (config validation) is responsible for enforcing mutual exclusion.
type SweeperPolicy struct {
	MaxAge   time.Duration
	MaxCount int
}

// Sweep deletes rows from both captured_requests and events according to the
// policy. Both tables are trimmed inside a single transaction so a mid-sweep
// cancellation cannot leave the two tables inconsistent.
//
// Time-based (MaxAge > 0): rows whose timestamp (UnixNano) is older than
// now-MaxAge are deleted from both tables.
//
// Count-based (MaxCount > 0): all but the MaxCount most-recent rows by
// timestamp are deleted from each table independently.
//
// When both fields are zero, Sweep is a no-op and returns 0, nil.
//
// After a successful sweep, PRAGMA wal_checkpoint(TRUNCATE) is run to
// reclaim space from the WAL file. Reclaiming space already committed to
// the main DB file requires a one-off operator VACUUM; the sweeper does
// not run VACUUM on its own because it is too expensive for the cadence.
func (s *SQLiteSink) Sweep(ctx context.Context, pol SweeperPolicy) (int64, error) {
	if pol.MaxAge == 0 && pol.MaxCount == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}

	var total int64

	if pol.MaxAge > 0 {
		cutoff := time.Now().UnixNano() - pol.MaxAge.Nanoseconds()

		res, err := tx.ExecContext(ctx,
			`DELETE FROM captured_requests WHERE timestamp < ?`, cutoff)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		n, _ := res.RowsAffected()
		total += n

		res, err = tx.ExecContext(ctx,
			`DELETE FROM events WHERE timestamp < ?`, cutoff)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		n, _ = res.RowsAffected()
		total += n
	} else {
		// MaxCount > 0
		res, err := tx.ExecContext(ctx,
			`DELETE FROM captured_requests WHERE id NOT IN (
				SELECT id FROM captured_requests ORDER BY timestamp DESC LIMIT ?
			)`, pol.MaxCount)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		n, _ := res.RowsAffected()
		total += n

		res, err = tx.ExecContext(ctx,
			`DELETE FROM events WHERE id NOT IN (
				SELECT id FROM events ORDER BY timestamp DESC LIMIT ?
			)`, pol.MaxCount)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		n, _ = res.RowsAffected()
		total += n
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// Reclaim WAL space after a successful sweep. TRUNCATE resets the WAL
	// file to zero length when all frames have been checkpointed. Errors
	// here are non-fatal: the data was already trimmed.
	_, _ = s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)

	return total, nil
}

// StartSweeper spawns a goroutine that calls Sweep at the given interval until
// ctx is cancelled. If interval is zero, defaultSweeperInterval is used. On
// sweep error the error is logged at WARN and the sweeper continues.
func (s *SQLiteSink) StartSweeper(ctx context.Context, pol SweeperPolicy, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = defaultSweeperInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				deleted, err := s.Sweep(ctx, pol)
				if err != nil {
					logger.Warn("sqlite retention sweep failed", "err", err)
				} else if deleted > 0 {
					logger.Info("sqlite retention sweep completed", "deleted", deleted)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}
