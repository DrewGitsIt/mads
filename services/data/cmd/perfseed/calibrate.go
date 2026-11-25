//go:build perf

package main

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"sort"

	_ "modernc.org/sqlite" // the store's own CGO-free driver
)

// openReadOnly opens an existing database strictly read-only: mode=ro on the
// URI plus query_only, so neither the driver nor a stray statement can
// write. It never runs migrations and never creates the file.
func openReadOnly(path string) (*sql.DB, error) {
	dsn := "file:" + url.PathEscape(path) + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// calibrate reads a database read-only and returns a profile with its
// per-type event counts and payload sizes and its record counts. Only
// counts and lengths are read: no payload content leaves SQLite.
func calibrate(ctx context.Context, db *sql.DB, name string) (*Profile, error) {
	p := &Profile{Name: name, Seed: 1, Events: map[string]TypeStats{}, WatchedRepos: 1, RunningDelegates: 3}

	rows, err := db.QueryContext(ctx,
		`SELECT type, COALESCE(length(payload), 0), session_id IS NULL FROM events ORDER BY type`)
	if err != nil {
		return nil, fmt.Errorf("calibrate: events: %w", err)
	}
	sizes := map[string][]int{}
	globals := map[string]int{}
	for rows.Next() {
		var (
			typ    string
			n      int
			global bool
		)
		if err := rows.Scan(&typ, &n, &global); err != nil {
			_ = rows.Close()
			return nil, err
		}
		sizes[typ] = append(sizes[typ], n)
		if global {
			globals[typ]++
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for typ, s := range sizes {
		p.Events[typ] = statsOf(s, globals[typ])
	}

	one := func(q string) (int, error) {
		var n sql.NullInt64
		err := db.QueryRowContext(ctx, q).Scan(&n)
		return int(n.Int64), err
	}
	for _, c := range []struct {
		dst *int
		q   string
	}{
		{&p.Sessions, `SELECT count(*) FROM sessions`},
		{&p.ActiveSessions, `SELECT count(*) FROM sessions WHERE activity = 'active'`},
		{&p.Delegates, `SELECT count(*) FROM delegates`},
		{&p.DelegateSessions, `SELECT count(DISTINCT session_id) FROM delegates`},
		{&p.Links, `SELECT count(*) FROM links`},
	} {
		if *c.dst, err = one(c.q); err != nil {
			return nil, fmt.Errorf("calibrate: %s: %w", c.q, err)
		}
	}
	p.RunningDelegates = min(p.RunningDelegates, p.Delegates)
	p.Links = min(p.Links, max(p.Sessions-1, 0))

	// The heaviest session's share of subagent events (the bulk of the log
	// and of scenario 3), fitted to a Zipf exponent over the seeded
	// sessions.
	share, err := topSessionShare(ctx, db, "session.subagent_event")
	if err != nil {
		return nil, err
	}
	p.SessionSkew = fitZipf(p.Sessions, share)
	p.Description = fmt.Sprintf("calibrated read-only from a live database: %d events; top session holds %.1f%% of session.subagent_event",
		p.TotalEvents(), share*100)
	return p, nil
}

func topSessionShare(ctx context.Context, db *sql.DB, typ string) (float64, error) {
	var top, total sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT max(c), sum(c) FROM (SELECT count(*) c FROM events WHERE type = ? AND session_id IS NOT NULL GROUP BY session_id)`,
		typ).Scan(&top, &total)
	if err != nil {
		return 0, fmt.Errorf("calibrate: top session share: %w", err)
	}
	if total.Int64 == 0 {
		return 0, nil
	}
	return float64(top.Int64) / float64(total.Int64), nil
}

func statsOf(sizes []int, globals int) TypeStats {
	sort.Ints(sizes)
	sum := 0
	for _, n := range sizes {
		sum += n
	}
	pct := func(q float64) int {
		if len(sizes) == 0 {
			return 0
		}
		i := int(math.Ceil(q*float64(len(sizes)))) - 1
		return sizes[max(0, min(i, len(sizes)-1))]
	}
	s := TypeStats{Count: len(sizes), P50: pct(0.50), P95: pct(0.95)}
	if len(sizes) > 0 {
		s.Mean = math.Round(float64(sum)/float64(len(sizes))*10) / 10
		s.Max = sizes[len(sizes)-1]
		s.GlobalFrac = math.Round(float64(globals)/float64(len(sizes))*1000) / 1000
	}
	return s
}

// zipfWeights returns n weights 1/(i+1)^s, normalised to sum to 1.
func zipfWeights(n int, s float64) []float64 {
	w := make([]float64, n)
	sum := 0.0
	for i := range w {
		w[i] = 1 / math.Pow(float64(i+1), s)
		sum += w[i]
	}
	for i := range w {
		w[i] /= sum
	}
	return w
}

// fitZipf finds the exponent whose top weight over n sessions is share.
func fitZipf(n int, share float64) float64 {
	if n <= 1 || share <= 1/float64(n) {
		return 0
	}
	lo, hi := 0.0, 8.0
	for range 60 {
		mid := (lo + hi) / 2
		if zipfWeights(n, mid)[0] < share {
			lo = mid
		} else {
			hi = mid
		}
	}
	return math.Round((lo+hi)/2*1000) / 1000
}
