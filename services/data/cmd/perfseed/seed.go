package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// actor stamps every event the seeder writes itself; the record stores
// stamp theirs with the same value.
const actor = "perfseed"

// chunk caps one bulk transaction. Large enough that commits are a rounding
// error, small enough that the WAL stays modest.
const chunk = 20000

type seedSession struct {
	idx       int
	id, name  string
	delegates []*seedDelegate
}

type seedDelegate struct {
	session     *seedSession
	harnessID   string
	toolUseID   string
	typ         string
	description string
	running     bool // left running for the Router's boot sweep to conclude
	tools       int
	taskEvents  int
	puts        int
	done        int // puts written so far
	start, end  float64
	descPad     int
}

// SeedOptions are the knobs a seed run takes beyond the profile.
type SeedOptions struct {
	// RepoDir is the workspace's primary repo and every session's working
	// directory; repo.changed events name it.
	RepoDir string
	// Now anchors timestamps: synthetic events span the 30 days before it.
	Now time.Time
	// TranscriptsDir, when set, makes the parent session a claude-code one
	// and writes its first running delegate's own transcript under it, laid
	// out like ~/.claude/projects (<project>/<ref>/subagents/agent-<id>.jsonl):
	// the harness copies it into each stack's scratch $HOME, so opening that
	// delegate takes the live path (its file), and the second running one —
	// with no file — the fallback (the log). The file mirrors the delegate's
	// own session.subagent_event history.
	TranscriptsDir string
}

// Manifest tells the harness what was seeded: which sessions and delegates
// its scenarios click, and the exact per-type counts and bytes written.
type Manifest struct {
	Profile     string           `json:"profile"`
	Seed        uint64           `json:"seed"`
	WorkspaceID string           `json:"workspace_id"`
	RepoDir     string           `json:"repo_dir"`
	Sessions    []ManifestRef    `json:"sessions"`
	Parent      ManifestParent   `json:"parent"`
	SwitchA     ManifestRef      `json:"switch_a"`
	SwitchB     ManifestRef      `json:"switch_b"`
	Counts      map[string]int   `json:"counts"`
	Bytes       map[string]int64 `json:"bytes"`
	TotalEvents int              `json:"total_events"`
	TotalBytes  int64            `json:"total_bytes"`
	Skipped     []string         `json:"skipped_types,omitempty"`
	Notes       []string         `json:"notes,omitempty"`
	ElapsedMS   int64            `json:"elapsed_ms"`
}

type ManifestRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ManifestParent struct {
	ManifestRef
	// Delegates is every delegate of the parent; Running the ones the
	// Router's boot sweep will conclude (and so show in the rail).
	Delegates int                `json:"delegates"`
	Running   []ManifestDelegate `json:"running"`
	// WithFile has a transcript file (scenario 3); NoFile has none (3b).
	WithFile *ManifestDelegate `json:"with_file,omitempty"`
	NoFile   *ManifestDelegate `json:"no_file,omitempty"`
}

type ManifestDelegate struct {
	HarnessID   string `json:"harness_id"`
	ToolUseID   string `json:"tool_use_id"`
	Description string `json:"description"`
}

// Seed writes p into a fresh database at path. It refuses an existing file:
// a workload must never land on top of real data.
func Seed(ctx context.Context, path string, p *Profile, opt SeedOptions) (*Manifest, error) {
	started := time.Now()
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("seed: %s already exists; the seeder only writes fresh databases", path)
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now().UTC()
	}
	db, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	// The Data Service's own boot: migrations plus whatever Bootstrap seeds.
	if err := store.Bootstrap(ctx, db); err != nil {
		return nil, err
	}

	rng := rand.New(rand.NewPCG(p.Seed, p.Seed^0x9e3779b97f4a7c15))
	m := &Manifest{Profile: p.Name, Seed: p.Seed, RepoDir: opt.RepoDir}
	if f := p.Fixtures; f.PendingPermissions+f.QueuedMessages+f.ProvisioningSessions > 0 {
		m.Notes = append(m.Notes, "fixtures are not seeded yet (phase 5); ignored")
	}
	log := store.NewLog(db)
	sessions := store.NewSessions(log)
	base := opt.Now.Add(-30 * 24 * time.Hour)

	// --- Records, through the stores, so rows and their events agree.
	ws := &contracts.Workspace{Name: "perf workspace", Repos: []contracts.WorkspaceRepo{{Path: opt.RepoDir, Primary: true}}}
	if err := store.NewWorkspaces(log).Create(ctx, ws, actor); err != nil {
		return nil, fmt.Errorf("seed: workspace: %w", err)
	}
	m.WorkspaceID = ws.ID
	cfgs := store.NewConfig(log)
	cfg, err := cfgs.Get(ctx)
	if err != nil {
		return nil, err
	}
	cfg.DefaultWorkspace, cfg.DefaultProvider, cfg.OnboardingComplete = ws.ID, "fake", true
	if err := cfgs.Update(ctx, cfg, actor); err != nil {
		return nil, fmt.Errorf("seed: config: %w", err)
	}

	fill := newFiller(rng)
	parentRef := ""
	ss := make([]*seedSession, p.Sessions)
	for i := range ss {
		s := &seedSession{idx: i, id: hexID(rng, "ses"), name: sessionName(i)}
		ss[i] = s
		act := contracts.SessionInactive
		if i < p.ActiveSessions {
			act = contracts.SessionActive
		}
		rec := &contracts.Session{
			ID: s.id, Name: s.name, Activity: act, Workspace: ws.ID, Provider: "fake",
			Isolation: contracts.IsolationCurrentDir, Cwd: opt.RepoDir, Dir: opt.RepoDir,
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if i == 0 && opt.TranscriptsDir != "" {
			rec.Provider, rec.ProviderRef = "claude-code", fmt.Sprintf("perf-%016x", rng.Uint64())
			parentRef = rec.ProviderRef
		}
		if err := sessions.Create(ctx, rec, actor); err != nil {
			return nil, fmt.Errorf("seed: session %d: %w", i, err)
		}
		m.Sessions = append(m.Sessions, ManifestRef{ID: s.id, Name: s.name})
	}

	links := store.NewLinks(log, sessions)
	var linkRecs []contracts.Link
	for i := 0; i < p.Links; i++ {
		// A chain of read links among the sessions after the scenario ones,
		// so no scenario's own session changes shape because of a link.
		g, sub := ss[(i+3)%p.Sessions], ss[(i+4)%p.Sessions]
		l := &contracts.Link{GrantingSessionID: g.id, SubjectSessionID: sub.id, Grants: []contracts.Grant{contracts.GrantRead}}
		if err := links.Create(ctx, l, actor); err != nil {
			return nil, fmt.Errorf("seed: link %d: %w", i, err)
		}
		linkRecs = append(linkRecs, *l)
	}

	// Delegates: spread over the delegate sessions by weight (the parent,
	// session 0, gets the most), each put several times so its record's
	// session.delegate_changed history has the profile's length.
	dels, err := planDelegates(rng, p, ss)
	if err != nil {
		return nil, err
	}

	// --- What's left of each type's count is synthetic log traffic.
	have, err := countsByType(ctx, db)
	if err != nil {
		return nil, err
	}
	// One closing session.agent_state per session (idle, through the store)
	// leaves each record's mirror matching its last event.
	closing := map[string]int{"session.agent_state": p.Sessions}
	puts := 0
	for _, d := range dels {
		puts += d.puts
	}
	type plan struct {
		typ   string
		n     int
		sizer *sizer
	}
	var plans []plan
	for _, typ := range p.sortedTypes() {
		st := p.Events[typ]
		if !isRegistered(typ) {
			if st.Count == 0 {
				continue
			}
			m.Skipped = append(m.Skipped, fmt.Sprintf("%s (%d events): not registered on this build", typ, st.Count))
			continue
		}
		if typ == string(contracts.SessionDelegateChanged) {
			continue // all of them come from planDelegates' puts
		}
		n := st.Count - have[typ] - closing[typ]
		if n < 0 {
			m.Notes = append(m.Notes, fmt.Sprintf("%s: records alone wrote %d, over the profile's %d", typ, have[typ]+closing[typ], st.Count))
			n = 0
		}
		plans = append(plans, plan{typ: typ, n: n, sizer: newSizer(rng, st, n)})
	}

	// The interleaved write order: one slot per synthetic event, plus one
	// per delegate put (a put is its own store transaction).
	const putSlot = -1
	var order []int
	for i, pl := range plans {
		for range pl.n {
			order = append(order, i)
		}
	}
	for range puts {
		order = append(order, putSlot)
	}
	rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	putQueue := orderPuts(dels)

	sessW := cdf(zipfWeights(p.Sessions, p.SessionSkew))
	delW := cdf(zipfWeights(p.DelegateSessions, p.SessionSkew))
	g := &genCtx{rng: rng, fill: fill, links: linkRecs, repoDir: opt.RepoDir}
	delegateStore := store.NewDelegates(log)
	total := float64(len(order))
	window := opt.Now.Sub(base)
	at := func(i int) time.Time { return base.Add(time.Duration(float64(window) * float64(i) / total)) }

	// The delegate whose transcript file gets written, and its stream.
	var fileDelegate *seedDelegate
	type fileEvent struct {
		payload []byte
		at      time.Time
	}
	var fileEvents []fileEvent
	if opt.TranscriptsDir != "" {
		fileDelegate = ss[0].delegates[0]
	}

	pickSession := func(typ string) (*seedSession, *seedDelegate) {
		switch typ {
		case "session.subagent_event", "session.subagent_task":
			s := ss[pick(rng, delW)]
			return s, s.delegates[rng.IntN(len(s.delegates))]
		}
		if p.Events[typ].GlobalFrac > 0 && rng.Float64() < p.Events[typ].GlobalFrac {
			return nil, nil
		}
		return ss[pick(rng, sessW)], nil
	}

	for i := 0; i < len(order); {
		if order[i] == putSlot {
			d := putQueue[0]
			putQueue = putQueue[1:]
			if err := putDelegate(ctx, delegateStore, d, at(i), fill); err != nil {
				return nil, err
			}
			i++
			continue
		}
		j := i
		err := log.WithTx(ctx, func(tx *store.Tx) error {
			for ; j < len(order) && order[j] != putSlot && j-i < chunk; j++ {
				pl := plans[order[j]]
				s, d := pickSession(pl.typ)
				g.session, g.delegate, g.at = s, d, at(j)
				env := &contracts.Envelope{Type: contracts.EventType(pl.typ), Actor: actor, Timestamp: g.at.UTC()}
				if s != nil {
					env.SessionID = s.id
				}
				// A session that was created and later deleted: its own id, never a live row's.
				if pl.typ == "session.created" || pl.typ == "session.deleted" {
					env.SessionID = hexID(rng, "ses")
				}
				payload, err := payloadFor(g, pl.typ, pl.sizer.take())
				if err != nil {
					return err
				}
				if d != nil && d == fileDelegate && pl.typ == "session.subagent_event" {
					fileEvents = append(fileEvents, fileEvent{payload: payload, at: g.at})
				}
				env.Payload = payload
				if err := tx.Append(env); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("seed: bulk events: %w", err)
		}
		i = j
	}

	for _, s := range ss {
		if err := sessions.SetAgentState(ctx, s.id, contracts.AgentState{State: contracts.AgentIdle, Since: opt.Now}, actor); err != nil {
			return nil, fmt.Errorf("seed: agent state %s: %w", s.id, err)
		}
	}

	parent := ss[0]
	m.Parent = ManifestParent{ManifestRef: ManifestRef{ID: parent.id, Name: parent.name}, Delegates: len(parent.delegates)}
	for _, d := range parent.delegates {
		if d.running {
			m.Parent.Running = append(m.Parent.Running, ManifestDelegate{HarnessID: d.harnessID, ToolUseID: d.toolUseID, Description: d.description})
		}
	}
	if fileDelegate != nil {
		var lines [][]byte
		for _, e := range fileEvents {
			lines = append(lines, e.payload)
		}
		ats := make([]time.Time, len(fileEvents))
		for i, e := range fileEvents {
			ats[i] = e.at
		}
		if err := writeDelegateTranscript(opt.TranscriptsDir, opt.RepoDir, parentRef, fileDelegate, lines, ats); err != nil {
			return nil, err
		}
		md := ManifestDelegate{HarnessID: fileDelegate.harnessID, ToolUseID: fileDelegate.toolUseID, Description: fileDelegate.description}
		m.Parent.WithFile = &md
		for _, d := range parent.delegates {
			if d.running && d != fileDelegate {
				nf := ManifestDelegate{HarnessID: d.harnessID, ToolUseID: d.toolUseID, Description: d.description}
				m.Parent.NoFile = &nf
				break
			}
		}
	}
	m.SwitchA = ManifestRef{ID: ss[1].id, Name: ss[1].name}
	m.SwitchB = ManifestRef{ID: ss[2%len(ss)].id, Name: ss[2%len(ss)].name}

	m.Counts, m.Bytes, err = statsByType(ctx, db)
	if err != nil {
		return nil, err
	}
	for t, n := range m.Counts {
		m.TotalEvents += n
		m.TotalBytes += m.Bytes[t]
	}
	// Leave a compact database with no WAL behind: every repetition copies it.
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return nil, err
	}
	m.ElapsedMS = time.Since(started).Milliseconds()
	return m, nil
}

func sessionName(i int) string {
	adj := []string{"amber", "brisk", "cobalt", "dune", "ember", "fjord", "gale", "harbor", "indigo", "juniper"}
	noun := []string{"refactor", "audit", "migration", "fix", "review", "spike", "cleanup", "rollout"}
	return fmt.Sprintf("perf %02d %s %s", i, adj[i%len(adj)], noun[(i/len(adj))%len(noun)])
}

// planDelegates builds every delegate and how many times it is put.
func planDelegates(rng *rand.Rand, p *Profile, ss []*seedSession) ([]*seedDelegate, error) {
	want := p.Events[string(contracts.SessionDelegateChanged)].Count
	if want < p.Delegates {
		return nil, fmt.Errorf("profile: session.delegate_changed (%d) below delegates (%d): every delegate is put at least once", want, p.Delegates)
	}
	// Per-session delegate counts by weight, at least one each.
	w := zipfWeights(p.DelegateSessions, p.SessionSkew)
	counts := make([]int, p.DelegateSessions)
	left := p.Delegates
	for i := range counts {
		counts[i] = 1
		left--
	}
	for i := range counts {
		extra := int(math.Floor(w[i] * float64(p.Delegates-p.DelegateSessions)))
		extra = min(extra, left)
		counts[i] += extra
		left -= extra
	}
	counts[0] += left
	if counts[0] < p.RunningDelegates {
		return nil, fmt.Errorf("profile: the parent session has %d delegates, fewer than running_delegates %d", counts[0], p.RunningDelegates)
	}

	types := []string{"general-purpose", "Explore", "Plan"}
	var all []*seedDelegate
	for si, n := range counts {
		s := ss[si]
		for k := range n {
			d := &seedDelegate{
				session:     s,
				harnessID:   fmt.Sprintf("a%016x", rng.Uint64()),
				toolUseID:   hexID(rng, "toolu"),
				typ:         types[rng.IntN(len(types))],
				description: fmt.Sprintf("perf delegate %02d-%03d", si, k),
				running:     si == 0 && k < p.RunningDelegates,
				puts:        1,
			}
			s.delegates = append(s.delegates, d)
			all = append(all, d)
		}
	}
	// Puts: one each, a second to end every finished one, the rest at random.
	extra := want - len(all)
	for _, d := range all {
		if extra == 0 {
			break
		}
		if !d.running {
			d.puts++
			extra--
		}
	}
	for ; extra > 0; extra-- {
		all[rng.IntN(len(all))].puts++
	}
	// Size: the profile's mean record, via the description's length (the
	// record's own fixed fields are ~450 bytes).
	mean := p.Events[string(contracts.SessionDelegateChanged)].Mean
	for _, d := range all {
		d.start = rng.Float64() * 0.9
		d.end = d.start + rng.Float64()*(1-d.start)
		d.descPad = max(0, int(mean)-520+rng.IntN(80)-40)
	}
	return all, nil
}

// orderPuts lists every put in write order: each delegate's puts spread
// evenly over its own lifetime in the log.
func orderPuts(dels []*seedDelegate) []*seedDelegate {
	type slot struct {
		at float64
		d  *seedDelegate
		k  int
	}
	var slots []slot
	for _, d := range dels {
		for k := range d.puts {
			f := d.start
			if d.puts > 1 {
				f += (d.end - d.start) * float64(k) / float64(d.puts-1)
			}
			slots = append(slots, slot{f, d, k})
		}
	}
	sort.SliceStable(slots, func(i, j int) bool {
		if slots[i].at != slots[j].at {
			return slots[i].at < slots[j].at
		}
		return slots[i].k < slots[j].k
	})
	out := make([]*seedDelegate, len(slots))
	for i, s := range slots {
		out[i] = s.d
	}
	return out
}

// putDelegate writes a delegate's next record through the store: running
// with a growing tool count, then (for a finished one) completed.
func putDelegate(ctx context.Context, st *store.Delegates, d *seedDelegate, at time.Time, fill *filler) error {
	d.done++
	last := d.done == d.puts
	startedAt := at.Add(-time.Duration(d.done) * time.Minute).UTC()
	rec := &contracts.Delegate{
		SessionID: d.session.id, HarnessID: d.harnessID, ToolUseID: d.toolUseID, Type: d.typ,
		Description: d.description, State: contracts.DelegateRunning, Runs: 1,
		StartedAt: startedAt, ToolUses: d.done * 3, DurationMS: d.done * 4000, TotalTokens: 2000 + d.done*500,
		Model:        "fake-model",
		Usage:        &contracts.Usage{InputTokens: 1000 + d.done*100, OutputTokens: 200 + d.done*20, Model: "fake-model"},
		Capabilities: &contracts.DelegateCapabilities{Transcript: false, Continuable: false, Steer: contracts.DelegateSteerNone, Stop: false},
	}
	if d.descPad > 0 {
		// The same text on every put: one task's description, sized so the
		// record matches the profile's mean.
		rec.Description = d.description + " — " + fill.pool[:d.descPad]
	}
	if last && !d.running {
		ended := at.UTC()
		rec.State, rec.EndedAt = contracts.DelegateCompleted, &ended
	}
	if err := st.Put(ctx, rec, actor); err != nil {
		return fmt.Errorf("seed: delegate %s put %d: %w", d.harnessID, d.done, err)
	}
	return nil
}

func cdf(w []float64) []float64 {
	out := make([]float64, len(w))
	sum := 0.0
	for i, v := range w {
		sum += v
		out[i] = sum
	}
	return out
}

func pick(rng *rand.Rand, c []float64) int {
	r := rng.Float64() * c[len(c)-1]
	i := sort.SearchFloat64s(c, r)
	return min(i, len(c)-1)
}

func countsByType(ctx context.Context, db *sql.DB) (map[string]int, error) {
	c, _, err := statsByType(ctx, db)
	return c, err
}

func statsByType(ctx context.Context, db *sql.DB) (map[string]int, map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, count(*), COALESCE(sum(length(payload)), 0) FROM events GROUP BY type`)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	counts, bytes := map[string]int{}, map[string]int64{}
	for rows.Next() {
		var (
			t string
			n int
			b int64
		)
		if err := rows.Scan(&t, &n, &b); err != nil {
			return nil, nil, err
		}
		counts[t], bytes[t] = n, b
	}
	return counts, bytes, rows.Err()
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
