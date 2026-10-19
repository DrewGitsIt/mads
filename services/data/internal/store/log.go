package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Log is the event log plus the transactional outbox (spec §15). All state
// mutations in the Data Service go through WithTx so the change and the
// events describing it commit atomically; subscribers are woken only after
// a successful commit.
type Log struct {
	db     *sql.DB
	notify notifier
}

// NewLog wraps an open, migrated database.
func NewLog(db *sql.DB) *Log {
	return &Log{db: db, notify: notifier{ch: make(chan struct{})}}
}

// Tx is an open outbox transaction: state writes via SQL plus event appends,
// committed together.
type Tx struct {
	tx       *sql.Tx
	appended bool
	// leaseToken is the request's lease proof (WithLeaseToken), read by
	// checkAndRenew to authorise a write to a leased task.
	leaseToken string
}

// Exec runs a state mutation inside the transaction.
func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.Exec(query, args...)
}

// QueryRow reads inside the transaction.
func (t *Tx) QueryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRow(query, args...)
}

// Query reads multiple rows inside the transaction.
func (t *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.tx.Query(query, args...)
}

// Append writes an event into the log within this transaction, assigning
// env.Seq and stamping env.Timestamp (UTC) if unset. The event is invisible
// to subscribers until the transaction commits.
func (t *Tx) Append(env *contracts.Envelope) error {
	if !env.Type.Valid() {
		return fmt.Errorf("log: unregistered event type %q", env.Type)
	}
	if env.Actor == "" {
		return fmt.Errorf("log: event %q has no actor", env.Type)
	}
	if env.Timestamp.IsZero() {
		env.Timestamp = time.Now().UTC()
	}
	var payload any
	if len(env.Payload) > 0 {
		payload = string(env.Payload)
	}
	res, err := t.tx.Exec(
		`INSERT INTO events (timestamp, type, session_id, task_id, actor, payload)
		 VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)`,
		env.Timestamp.Format(time.RFC3339Nano), string(env.Type),
		env.SessionID, env.TaskID, env.Actor, payload,
	)
	if err != nil {
		return fmt.Errorf("log: append %s: %w", env.Type, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("log: append %s: %w", env.Type, err)
	}
	env.Seq = seq
	t.appended = true
	return nil
}

// WithTx runs fn inside one transaction — the outbox boundary. If fn returns
// an error, every state write and event in it rolls back together. On commit,
// subscribers are notified if any events were appended.
func (l *Log) WithTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("log: begin: %w", err)
	}
	t := &Tx{tx: tx, leaseToken: leaseTokenFrom(ctx)}
	if err := fn(t); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("log: commit: %w", err)
	}
	if t.appended {
		l.notify.broadcast()
	}
	return nil
}

// Append writes a standalone event (no accompanying state change) in its own
// transaction.
func (l *Log) Append(ctx context.Context, env *contracts.Envelope) error {
	return l.WithTx(ctx, func(t *Tx) error { return t.Append(env) })
}

// Filter selects events for Query. Zero values mean "no constraint".
type Filter struct {
	// Since returns only events with seq strictly greater — the resume cursor.
	Since int64
	// Until, when nonzero, caps the upper bound (inclusive) — pipeline-run
	// reconstruction buckets events by seq range (spec §8, W5.7).
	Until int64
	// SessionID restricts to one session's events.
	SessionID string
	// Types is a list of patterns: exact ("task.created") or domain wildcard
	// ("task.*"), matching contracts.EventType.MatchesFilter.
	Types []string
	// Limit caps the batch; 0 means no cap.
	Limit int
}

// Query returns matching events in seq order. Filtering happens in SQL
// against the log's indexes — subscribers never sift another session's mail
// (spec §15).
func (l *Log) Query(ctx context.Context, f Filter) ([]contracts.Envelope, error) {
	var (
		where = []string{"seq > ?"}
		args  = []any{f.Since}
	)
	if f.Until > 0 {
		where = append(where, "seq <= ?")
		args = append(args, f.Until)
	}
	if f.SessionID != "" {
		where = append(where, "session_id = ?")
		args = append(args, f.SessionID)
	}
	if len(f.Types) > 0 {
		var clauses []string
		for _, p := range f.Types {
			if dom, ok := strings.CutSuffix(p, ".*"); ok {
				clauses = append(clauses, "type LIKE ?")
				args = append(args, dom+".%")
			} else {
				clauses = append(clauses, "type = ?")
				args = append(args, p)
			}
		}
		where = append(where, "("+strings.Join(clauses, " OR ")+")")
	}
	q := `SELECT seq, timestamp, type, session_id, task_id, actor, payload
	      FROM events WHERE ` + strings.Join(where, " AND ") + ` ORDER BY seq`
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("log: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []contracts.Envelope
	for rows.Next() {
		var (
			env       contracts.Envelope
			ts        string
			sessionID sql.NullString
			taskID    sql.NullString
			payload   sql.NullString
		)
		if err := rows.Scan(&env.Seq, &ts, &env.Type, &sessionID, &taskID, &env.Actor, &payload); err != nil {
			return nil, fmt.Errorf("log: scan: %w", err)
		}
		env.Timestamp, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, fmt.Errorf("log: bad timestamp on seq %d: %w", env.Seq, err)
		}
		env.SessionID = sessionID.String
		env.TaskID = taskID.String
		if payload.Valid {
			env.Payload = []byte(payload.String)
		}
		out = append(out, env)
	}
	return out, rows.Err()
}

// Wait returns a channel that closes the next time any events commit. Callers
// query from their cursor, then Wait, then query again — the notification
// carries no data, it only wakes.
func (l *Log) Wait() <-chan struct{} {
	return l.notify.wait()
}

// notifier is a broadcast latch: broadcast closes the current channel and
// replaces it, waking every waiter at once.
type notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

func (n *notifier) wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ch
}

func (n *notifier) broadcast() {
	n.mu.Lock()
	defer n.mu.Unlock()
	close(n.ch)
	n.ch = make(chan struct{})
}
