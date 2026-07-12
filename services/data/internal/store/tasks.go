package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// ErrNotFound is returned when a task (or sub-record) does not exist.
var ErrNotFound = errors.New("not found")

// Tasks is the task store (spec §3). Every mutation goes through the outbox:
// the row change and the events describing it commit in one transaction.
type Tasks struct {
	log *Log
}

// NewTasks wraps the event log's database.
func NewTasks(log *Log) *Tasks {
	return &Tasks{log: log}
}

// Validate checks a task for storage, reporting every problem at once
// (spec §10.3). Dependency existence is checked separately, inside the
// writing transaction.
func Validate(t *contracts.Task) *contracts.ValidationError {
	var v contracts.ValidationError
	if t.Title == "" {
		v.Add("title", "required")
	}
	if t.SessionID == "" {
		v.Add("session_id", "required")
	}
	if t.Source == "" {
		v.Add("source", "required")
	}
	if t.Status != "" && !t.Status.Valid() {
		v.Add("status", fmt.Sprintf("unknown status %q", t.Status))
	}
	for i, n := range t.Notes {
		if n.Content == "" {
			v.Add(fmt.Sprintf("notes[%d].content", i), "required")
		}
	}
	if v.Empty() {
		return nil
	}
	return &v
}

// Create stores a new task, assigning its ID and timestamps, and emits
// task.created. Actor is who performed the creation ("user", "synthesizer",
// an agent name); status defaults to ready.
func (s *Tasks) Create(ctx context.Context, t *contracts.Task, actor string) error {
	if verr := Validate(t); verr != nil {
		return verr
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		return s.createInTx(tx, t, actor)
	})
}

// Import stores several tasks atomically (spec §4 bulk import): either every
// task lands with its task.created event, or none do. Dependencies may point
// at other tasks in the same batch.
func (s *Tasks) Import(ctx context.Context, ts []*contracts.Task, actor string) error {
	var v contracts.ValidationError
	for i, t := range ts {
		if verr := Validate(t); verr != nil {
			for _, fe := range verr.Errors {
				v.Add(fmt.Sprintf("tasks[%d].%s", i, fe.Field), fe.Message)
			}
		}
	}
	if !v.Empty() {
		return &v
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		for _, t := range ts {
			if err := s.createInTx(tx, t, actor); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Tasks) createInTx(tx *Tx, t *contracts.Task, actor string) error {
	if t.ID == "" {
		t.ID = NewID("tsk")
	}
	if t.Status == "" {
		t.Status = contracts.StatusReady
	}
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	labels, err := json.Marshal(orEmpty(t.Labels))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO tasks (id, session_id, status, retry_count, created_at, updated_at,
		                    title, description, acceptance_criteria, scope, draft, source, labels)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.SessionID, string(t.Status), t.RetryCount,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		t.Title, t.Description, t.AcceptanceCriteria, t.Scope,
		boolToInt(t.Draft), t.Source, string(labels),
	); err != nil {
		return fmt.Errorf("tasks: insert %s: %w", t.ID, err)
	}
	if err := writeDeps(tx, t.ID, t.SessionID, t.Dependencies); err != nil {
		return err
	}
	for i := range t.Handoffs {
		if err := insertHandoff(tx, t.ID, &t.Handoffs[i]); err != nil {
			return err
		}
	}
	for i := range t.Notes {
		if err := insertNote(tx, t.ID, &t.Notes[i]); err != nil {
			return err
		}
	}
	payload, _ := json.Marshal(map[string]any{"title": t.Title, "draft": t.Draft})
	return tx.Append(&contracts.Envelope{
		Type: contracts.TaskCreated, SessionID: t.SessionID, TaskID: t.ID,
		Actor: actor, Payload: payload,
	})
}

// Update replaces a task's mutable fields (everything: spec §3 "fully
// mutable"). Emits task.updated, plus task.status_changed when the status
// differs. Handoffs and notes are managed by their own methods and are not
// touched here.
func (s *Tasks) Update(ctx context.Context, t *contracts.Task, actor string) error {
	if verr := Validate(t); verr != nil {
		return verr
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		var oldStatus string
		err := tx.QueryRow("SELECT status FROM tasks WHERE id = ?", t.ID).Scan(&oldStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tasks: %s: %w", t.ID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := checkAndRenew(tx, t.ID, actor, now); err != nil {
			return err
		}
		t.UpdatedAt = now
		labels, err := json.Marshal(orEmpty(t.Labels))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`UPDATE tasks SET session_id=?, status=?, retry_count=?, updated_at=?,
			        title=?, description=?, acceptance_criteria=?, scope=?, draft=?, source=?, labels=?
			 WHERE id=?`,
			t.SessionID, string(t.Status), t.RetryCount, now.Format(time.RFC3339Nano),
			t.Title, t.Description, t.AcceptanceCriteria, t.Scope,
			boolToInt(t.Draft), t.Source, string(labels), t.ID,
		); err != nil {
			return fmt.Errorf("tasks: update %s: %w", t.ID, err)
		}
		if _, err := tx.Exec("DELETE FROM task_deps WHERE task_id = ?", t.ID); err != nil {
			return err
		}
		if err := writeDeps(tx, t.ID, t.SessionID, t.Dependencies); err != nil {
			return err
		}
		if err := tx.Append(&contracts.Envelope{
			Type: contracts.TaskUpdated, SessionID: t.SessionID, TaskID: t.ID, Actor: actor,
		}); err != nil {
			return err
		}
		if oldStatus != string(t.Status) {
			payload, _ := json.Marshal(map[string]string{"from": oldStatus, "to": string(t.Status)})
			return tx.Append(&contracts.Envelope{
				Type: contracts.TaskStatusChanged, SessionID: t.SessionID, TaskID: t.ID,
				Actor: actor, Payload: payload,
			})
		}
		return nil
	})
}

// Confirm turns a draft into a real task (spec §4), emitting task.confirmed.
func (s *Tasks) Confirm(ctx context.Context, id, actor string) error {
	return s.draftTransition(ctx, id, actor, contracts.TaskConfirmed, func(tx *Tx, sessionID string) error {
		_, err := tx.Exec("UPDATE tasks SET draft = 0, updated_at = ? WHERE id = ?",
			time.Now().UTC().Format(time.RFC3339Nano), id)
		return err
	})
}

// Discard deletes a draft (spec §4), emitting task.discarded. Non-draft
// tasks cannot be discarded — cancel them with status wont_do instead.
func (s *Tasks) Discard(ctx context.Context, id, actor string) error {
	return s.draftTransition(ctx, id, actor, contracts.TaskDiscarded, func(tx *Tx, sessionID string) error {
		_, err := tx.Exec("DELETE FROM tasks WHERE id = ?", id)
		return err
	})
}

func (s *Tasks) draftTransition(ctx context.Context, id, actor string, event contracts.EventType, apply func(*Tx, string) error) error {
	return s.log.WithTx(ctx, func(tx *Tx) error {
		var (
			sessionID string
			draft     int
		)
		err := tx.QueryRow("SELECT session_id, draft FROM tasks WHERE id = ?", id).Scan(&sessionID, &draft)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tasks: %s: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if err := checkAndRenew(tx, id, actor, time.Now().UTC()); err != nil {
			return err
		}
		if draft == 0 {
			verr := &contracts.ValidationError{}
			verr.Add("draft", "task is not a draft")
			return verr
		}
		if err := apply(tx, sessionID); err != nil {
			return err
		}
		return tx.Append(&contracts.Envelope{Type: event, SessionID: sessionID, TaskID: id, Actor: actor})
	})
}

// AddHandoff appends stage-to-stage context (spec §6), emitting task.updated.
func (s *Tasks) AddHandoff(ctx context.Context, taskID string, h *contracts.Handoff, actor string) error {
	if h.Content == "" || h.From == "" {
		verr := &contracts.ValidationError{}
		if h.From == "" {
			verr.Add("from", "required")
		}
		if h.Content == "" {
			verr.Add("content", "required")
		}
		return verr
	}
	return s.subRecord(ctx, taskID, actor, contracts.TaskUpdated, func(tx *Tx) error {
		return insertHandoff(tx, taskID, h)
	})
}

// AddNote attaches a typed note (spec §4), emitting note.created.
func (s *Tasks) AddNote(ctx context.Context, taskID string, n *contracts.Note, actor string) error {
	var verr contracts.ValidationError
	if n.Content == "" {
		verr.Add("content", "required")
	}
	if n.Type == "" {
		verr.Add("type", "required")
	}
	if !verr.Empty() {
		return &verr
	}
	return s.subRecord(ctx, taskID, actor, contracts.NoteCreated, func(tx *Tx) error {
		return insertNote(tx, taskID, n)
	})
}

func (s *Tasks) subRecord(ctx context.Context, taskID, actor string, event contracts.EventType, apply func(*Tx) error) error {
	return s.log.WithTx(ctx, func(tx *Tx) error {
		var sessionID string
		err := tx.QueryRow("SELECT session_id FROM tasks WHERE id = ?", taskID).Scan(&sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tasks: %s: %w", taskID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if err := checkAndRenew(tx, taskID, actor, time.Now().UTC()); err != nil {
			return err
		}
		if err := apply(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE tasks SET updated_at = ? WHERE id = ?",
			time.Now().UTC().Format(time.RFC3339Nano), taskID); err != nil {
			return err
		}
		return tx.Append(&contracts.Envelope{Type: event, SessionID: sessionID, TaskID: taskID, Actor: actor})
	})
}

// Query selects tasks. Zero values mean "no constraint".
type TaskQuery struct {
	SessionID string
	Status    contracts.TaskStatus
	// Draft filters on draft state when non-nil.
	Draft *bool
}

// List returns matching tasks, oldest first, fully hydrated.
func (s *Tasks) List(ctx context.Context, q TaskQuery) ([]contracts.Task, error) {
	where, args := "1=1", []any{}
	if q.SessionID != "" {
		where += " AND session_id = ?"
		args = append(args, q.SessionID)
	}
	if q.Status != "" {
		where += " AND status = ?"
		args = append(args, string(q.Status))
	}
	if q.Draft != nil {
		where += " AND draft = ?"
		args = append(args, boolToInt(*q.Draft))
	}
	rows, err := s.log.db.QueryContext(ctx,
		`SELECT id, session_id, status, retry_count, created_at, updated_at,
		        title, description, acceptance_criteria, scope, draft, source, labels
		 FROM tasks WHERE `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("tasks: list: %w", err)
	}

	var out []contracts.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, *t)
	}
	// Close before hydrating: the pool holds a single connection (store.Open),
	// so a second query while this cursor is open would deadlock.
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.hydrate(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Get returns one task, fully hydrated.
func (s *Tasks) Get(ctx context.Context, id string) (*contracts.Task, error) {
	rows, err := s.log.db.QueryContext(ctx,
		`SELECT id, session_id, status, retry_count, created_at, updated_at,
		        title, description, acceptance_criteria, scope, draft, source, labels
		 FROM tasks WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("tasks: get %s: %w", id, err)
	}
	var t *contracts.Task
	if rows.Next() {
		t, err = scanTask(rows)
	}
	// Close before hydrating — single-connection pool, see List.
	if err := errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("tasks: %s: %w", id, ErrNotFound)
	}
	if err := s.hydrate(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

// Cycles finds dependency cycles among a session's tasks (spec §3.6). Cycles
// are legal at rest; pipeline launch calls this and refuses to start while
// any exist. Each cycle is returned as the task IDs along it.
func (s *Tasks) Cycles(ctx context.Context, sessionID string) ([][]string, error) {
	rows, err := s.log.db.QueryContext(ctx,
		`SELECT d.task_id, d.depends_on FROM task_deps d
		 JOIN tasks t ON t.id = d.task_id WHERE t.session_id = ?`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("tasks: cycles: %w", err)
	}
	defer func() { _ = rows.Close() }()

	adj := map[string][]string{}
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, err
		}
		adj[from] = append(adj[from], to)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return findCycles(adj), nil
}

// findCycles runs DFS with an explicit stack coloring, collecting one cycle
// per back edge.
func findCycles(adj map[string][]string) [][]string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	onPath := []string{}
	var cycles [][]string

	var visit func(n string)
	visit = func(n string) {
		color[n] = gray
		onPath = append(onPath, n)
		for _, m := range adj[n] {
			switch color[m] {
			case white:
				visit(m)
			case gray:
				// Back edge: the cycle is the path suffix from m to n.
				for i, p := range onPath {
					if p == m {
						cycle := append([]string{}, onPath[i:]...)
						cycles = append(cycles, cycle)
						break
					}
				}
			}
		}
		onPath = onPath[:len(onPath)-1]
		color[n] = black
	}

	nodes := make([]string, 0, len(adj))
	for n := range adj {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes) // deterministic traversal → deterministic cycle report
	for _, n := range nodes {
		if color[n] == white {
			visit(n)
		}
	}
	return cycles
}

// --- helpers ---

func writeDeps(tx *Tx, taskID, sessionID string, deps []string) error {
	var verr contracts.ValidationError
	for i, dep := range deps {
		if dep == taskID {
			verr.Add(fmt.Sprintf("dependencies[%d]", i), "task cannot depend on itself")
			continue
		}
		var depSession string
		err := tx.QueryRow("SELECT session_id FROM tasks WHERE id = ?", dep).Scan(&depSession)
		if errors.Is(err, sql.ErrNoRows) {
			verr.Add(fmt.Sprintf("dependencies[%d]", i), fmt.Sprintf("task %q does not exist", dep))
			continue
		}
		if err != nil {
			return err
		}
		if depSession != sessionID {
			verr.Add(fmt.Sprintf("dependencies[%d]", i),
				fmt.Sprintf("task %q belongs to another session; dependencies stay within a session", dep))
			continue
		}
		if _, err := tx.Exec("INSERT INTO task_deps (task_id, depends_on) VALUES (?, ?)", taskID, dep); err != nil {
			return err
		}
	}
	if verr.Empty() {
		return nil
	}
	return &verr
}

func insertHandoff(tx *Tx, taskID string, h *contracts.Handoff) error {
	if h.Timestamp.IsZero() {
		h.Timestamp = time.Now().UTC()
	}
	_, err := tx.Exec(
		"INSERT INTO task_handoffs (task_id, from_agent, content, timestamp) VALUES (?, ?, ?, ?)",
		taskID, h.From, h.Content, h.Timestamp.Format(time.RFC3339Nano))
	return err
}

func insertNote(tx *Tx, taskID string, n *contracts.Note) error {
	if n.Timestamp.IsZero() {
		n.Timestamp = time.Now().UTC()
	}
	if n.Status == "" {
		n.Status = contracts.NoteStatusConfirmed
	}
	_, err := tx.Exec(
		"INSERT INTO task_notes (task_id, type, content, source, status, timestamp) VALUES (?, ?, ?, ?, ?, ?)",
		taskID, string(n.Type), n.Content, n.Source, string(n.Status), n.Timestamp.Format(time.RFC3339Nano))
	return err
}

func scanTask(rows *sql.Rows) (*contracts.Task, error) {
	var (
		t                    contracts.Task
		createdAt, updatedAt string
		draft                int
		labels               string
	)
	if err := rows.Scan(&t.ID, &t.SessionID, &t.Status, &t.RetryCount, &createdAt, &updatedAt,
		&t.Title, &t.Description, &t.AcceptanceCriteria, &t.Scope, &draft, &t.Source, &labels); err != nil {
		return nil, fmt.Errorf("tasks: scan: %w", err)
	}
	var err error
	if t.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, err
	}
	if t.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return nil, err
	}
	t.Draft = draft != 0
	if err := json.Unmarshal([]byte(labels), &t.Labels); err != nil {
		return nil, fmt.Errorf("tasks: labels for %s: %w", t.ID, err)
	}
	return &t, nil
}

func (s *Tasks) hydrate(ctx context.Context, t *contracts.Task) error {
	deps, err := s.log.db.QueryContext(ctx,
		"SELECT depends_on FROM task_deps WHERE task_id = ? ORDER BY depends_on", t.ID)
	if err != nil {
		return err
	}
	defer func() { _ = deps.Close() }()
	for deps.Next() {
		var d string
		if err := deps.Scan(&d); err != nil {
			return err
		}
		t.Dependencies = append(t.Dependencies, d)
	}
	if err := deps.Err(); err != nil {
		return err
	}

	hs, err := s.log.db.QueryContext(ctx,
		"SELECT from_agent, content, timestamp FROM task_handoffs WHERE task_id = ? ORDER BY id", t.ID)
	if err != nil {
		return err
	}
	defer func() { _ = hs.Close() }()
	for hs.Next() {
		var (
			h  contracts.Handoff
			ts string
		)
		if err := hs.Scan(&h.From, &h.Content, &ts); err != nil {
			return err
		}
		if h.Timestamp, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			return err
		}
		t.Handoffs = append(t.Handoffs, h)
	}
	if err := hs.Err(); err != nil {
		return err
	}

	ns, err := s.log.db.QueryContext(ctx,
		"SELECT type, content, source, status, timestamp FROM task_notes WHERE task_id = ? ORDER BY id", t.ID)
	if err != nil {
		return err
	}
	defer func() { _ = ns.Close() }()
	for ns.Next() {
		var (
			n  contracts.Note
			ts string
		)
		if err := ns.Scan(&n.Type, &n.Content, &n.Source, &n.Status, &ts); err != nil {
			return err
		}
		if n.Timestamp, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			return err
		}
		t.Notes = append(t.Notes, n)
	}
	return ns.Err()
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
