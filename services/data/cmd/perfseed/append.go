//go:build perf

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// appendTypes are the types an open window sees arrive live over a working
// day: agent and delegate traffic, the job churn, file and commit noise.
// Record-backed types (session.created/renamed/deleted, delegate_changed,
// link.*, the registries) are left out: posting their event without the
// record would show the rail state no store wrote.
var appendTypes = map[string]bool{
	"session.subagent_event": true, "session.subagent_task": true,
	"job.queued": true, "job.started": true, "job.completed": true,
	"session.agent_state": true, "file.changed": true, "session.updated": true,
	"session.usage_updated": true, "session.turn_ended": true, "session.thinking": true,
	"commit.created": true, "session.activity_changed": true,
}

// AppendSummary is what `perfseed append` prints on stdout.
type AppendSummary struct {
	Events       int            `json:"events"`
	PayloadBytes int64          `json:"payload_bytes"`
	Counts       map[string]int `json:"counts"`
	ElapsedMS    int64          `json:"elapsed_ms"`
}

// runAppend posts n synthetic events through a running Data Service's
// POST /events, the way live producers do (scenario 8: a window left open
// for a long time). The type mix is the profile's, restricted to
// appendTypes; sessions are the manifest's, weighted as seed weights them;
// subagent events go to fresh synthetic delegates of the delegate sessions
// (never a delegate a scenario opens). Payloads come from payloadFor, so
// they are shaped and sized like the seeded ones. Ends with one idle
// session.agent_state per session (every turn ends). -rate paces the
// posts, so the window takes them as a stream rather than one burst.
func runAppend(args []string) error {
	fs := flag.NewFlagSet("append", flag.ExitOnError)
	url := fs.String("url", "", "the Data Service base URL (a test stack's, never a live port)")
	prof := fs.String("profile", "", "workload profile JSON (type mix and payload sizes)")
	man := fs.String("manifest", "", "the seed's manifest JSON (sessions); - for stdin")
	n := fs.Int("n", 50000, "events to append")
	conc := fs.Int("concurrency", 16, "parallel POSTs")
	seed := fs.Uint64("seed", 2, "rng seed")
	rate := fs.Float64("rate", 0, "events per second, paced evenly (0: as fast as the Data Service takes them)")
	_ = fs.Parse(args)
	if *url == "" || *prof == "" || *man == "" || *n <= 0 {
		return fmt.Errorf("append: -url, -profile, -manifest and -n are required")
	}
	for _, port := range []string{":17373", ":17374", ":17380"} {
		if bytes.Contains([]byte(*url), []byte(port)) {
			return fmt.Errorf("append: refusing %s: a live port", *url)
		}
	}
	p, err := loadProfile(*prof)
	if err != nil {
		return err
	}
	var mb []byte
	if *man == "-" {
		mb, err = io.ReadAll(os.Stdin)
	} else {
		mb, err = os.ReadFile(*man)
	}
	if err != nil {
		return err
	}
	var m Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return fmt.Errorf("append: manifest: %w", err)
	}
	if len(m.Sessions) == 0 {
		return fmt.Errorf("append: manifest has no sessions")
	}
	token := os.Getenv("MADS_PERF_TOKEN")

	rng := rand.New(rand.NewPCG(*seed, *seed^0x9e3779b97f4a7c15))
	ss := make([]*seedSession, len(m.Sessions))
	for i, s := range m.Sessions {
		ss[i] = &seedSession{idx: i, id: s.ID, name: s.Name}
	}
	nDel := min(p.DelegateSessions, len(ss))
	types := []string{"general-purpose", "Explore", "Plan"}
	for i := 0; i < nDel; i++ {
		for k := range 3 {
			ss[i].delegates = append(ss[i].delegates, &seedDelegate{
				session: ss[i], harnessID: fmt.Sprintf("a%016x", rng.Uint64()), toolUseID: hexID(rng, "toolu"),
				typ: types[rng.IntN(len(types))], description: fmt.Sprintf("perf append delegate %02d-%d", i, k), running: true,
			})
		}
	}

	// Per-type counts: the profile's shares of the appendable types.
	var typs []string
	total := 0
	for _, t := range p.sortedTypes() {
		if appendTypes[t] && isRegistered(t) && p.Events[t].Count > 0 {
			typs = append(typs, t)
			total += p.Events[t].Count
		}
	}
	if total == 0 {
		return fmt.Errorf("append: the profile has none of the appendable types")
	}
	counts := map[string]int{}
	left := *n
	for _, t := range typs {
		c := int(float64(*n) * float64(p.Events[t].Count) / float64(total))
		counts[t] = c
		left -= c
	}
	sort.SliceStable(typs, func(i, j int) bool { return p.Events[typs[i]].Count > p.Events[typs[j]].Count })
	counts[typs[0]] += left
	sizers := map[string]*sizer{}
	var order []string
	for _, t := range typs {
		sizers[t] = newSizer(rng, p.Events[t], counts[t])
		for range counts[t] {
			order = append(order, t)
		}
	}
	rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

	sessW := cdf(zipfWeights(len(ss), p.SessionSkew))
	delW := cdf(zipfWeights(nDel, p.SessionSkew))
	g := &genCtx{rng: rng, fill: newFiller(rng), repoDir: m.RepoDir}

	type job struct{ body []byte }
	jobs := make(chan job, 4*(*conc))
	client := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{MaxIdleConns: *conc * 2, MaxIdleConnsPerHost: *conc * 2, MaxConnsPerHost: *conc},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		firstErr error
		errOnce  sync.Once
		posted   atomic.Int64
	)
	post := func(body []byte) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, *url+"/events", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		b, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			return fmt.Errorf("POST /events: %d %s", res.StatusCode, bytes.TrimSpace(b[:min(len(b), 200)]))
		}
		posted.Add(1)
		return nil
	}
	var wg sync.WaitGroup
	for range *conc {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() != nil {
					continue
				}
				if err := post(j.body); err != nil {
					errOnce.Do(func() { firstErr = err; cancel() })
				}
			}
		}()
	}

	started := time.Now()
	sum := AppendSummary{Counts: map[string]int{}}
	envelope := func(typ string, s *seedSession, payload []byte) []byte {
		env := contracts.Envelope{Type: contracts.EventType(typ), Actor: actor, Timestamp: time.Now().UTC(), Payload: payload}
		if s != nil {
			env.SessionID = s.id
		}
		b, _ := json.Marshal(env)
		sum.Events++
		sum.PayloadBytes += int64(len(payload))
		sum.Counts[typ]++
		return b
	}
	for i, typ := range order {
		if ctx.Err() != nil {
			break
		}
		if *rate > 0 {
			if d := time.Until(started.Add(time.Duration(float64(i) / *rate * float64(time.Second)))); d > 0 {
				time.Sleep(d)
			}
		}
		var s *seedSession
		var d *seedDelegate
		switch typ {
		case "session.subagent_event", "session.subagent_task":
			s = ss[pick(rng, delW)]
			d = s.delegates[rng.IntN(len(s.delegates))]
		default:
			if st := p.Events[typ]; !(st.GlobalFrac > 0 && rng.Float64() < st.GlobalFrac) {
				s = ss[pick(rng, sessW)]
			}
		}
		g.session, g.delegate, g.at = s, d, time.Now()
		payload, err := payloadFor(g, typ, sizers[typ].take())
		if err != nil {
			cancel()
			close(jobs)
			wg.Wait()
			return err
		}
		jobs <- job{envelope(typ, s, payload)}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return fmt.Errorf("append: after %d events: %w", posted.Load(), firstErr)
	}
	// Every turn ends: each session idle again, so no rail card is left working.
	for _, s := range ss {
		b, _ := json.Marshal(contracts.AgentState{State: contracts.AgentIdle, Since: time.Now().UTC()})
		if err := post(envelope("session.agent_state", s, b)); err != nil {
			return fmt.Errorf("append: closing agent_state: %w", err)
		}
	}
	sum.ElapsedMS = time.Since(started).Milliseconds()
	fmt.Fprintf(os.Stderr, "perfseed: appended %d events (%.1f MB payload) in %.1fs\n", sum.Events, float64(sum.PayloadBytes)/1e6, float64(sum.ElapsedMS)/1000)
	return writeJSONTo(os.Stdout, sum)
}
