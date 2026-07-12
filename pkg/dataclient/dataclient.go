// Package dataclient is the HTTP client for the Data Service — the one way
// any other service touches persistent state (spec §15). It wraps the REST
// contract in pkg/contracts types, stamps every mutation with the caller's
// X-Mads-Actor identity, and maps the API's error shapes onto typed errors
// (ErrNotFound, ErrConflict, *contracts.ValidationError).
package dataclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// ErrNotFound maps 404s.
var ErrNotFound = errors.New("not found")

// ErrConflict maps 409s — for tasks, that's "live-leased by someone else".
var ErrConflict = errors.New("conflict")

type Client struct {
	base  string
	actor string
	http  *http.Client
}

// New builds a client. actor names the mutation identity this client rides
// under (attribution, not authentication — spec §15); empty means the
// server-side default, "user".
func New(baseURL, actor string) *Client {
	return &Client{
		base:  strings.TrimSuffix(baseURL, "/"),
		actor: actor,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// do runs one request; out, when non-nil, receives the decoded 2xx body.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.actor != "" {
		req.Header.Set("X-Mads-Actor", c.actor)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if out == nil {
			return nil
		}
		return json.Unmarshal(raw, out)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	case resp.StatusCode == http.StatusConflict:
		return fmt.Errorf("%s %s: %w: %s", method, path, ErrConflict, strings.TrimSpace(string(raw)))
	case resp.StatusCode == http.StatusUnprocessableEntity:
		var verr contracts.ValidationError
		if json.Unmarshal(raw, &verr) == nil && !verr.Empty() {
			return &verr
		}
		fallthrough
	default:
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
}

// --- Tasks (spec §3–§4) ---

func (c *Client) CreateTask(ctx context.Context, t *contracts.Task) error {
	return c.do(ctx, http.MethodPost, "/tasks", t, t)
}

// ImportTasks creates a batch atomically (intra-batch dependencies allowed).
func (c *Client) ImportTasks(ctx context.Context, ts []contracts.Task) ([]contracts.Task, error) {
	var wrapper struct {
		Tasks []contracts.Task `json:"tasks"`
	}
	wrapper.Tasks = ts
	err := c.do(ctx, http.MethodPost, "/tasks/import", wrapper, &wrapper)
	return wrapper.Tasks, err
}

func (c *Client) Task(ctx context.Context, id string) (contracts.Task, error) {
	var t contracts.Task
	err := c.do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(id), nil, &t)
	return t, err
}

// Tasks lists by session and optional status.
func (c *Client) Tasks(ctx context.Context, sessionID string, status contracts.TaskStatus) ([]contracts.Task, error) {
	q := url.Values{}
	if sessionID != "" {
		q.Set("session_id", sessionID)
	}
	if status != "" {
		q.Set("status", string(status))
	}
	var out []contracts.Task
	err := c.do(ctx, http.MethodGet, "/tasks?"+q.Encode(), nil, &out)
	return out, err
}

// UpdateTask is the API's full-replace PUT.
func (c *Client) UpdateTask(ctx context.Context, t *contracts.Task) error {
	return c.do(ctx, http.MethodPut, "/tasks/"+url.PathEscape(t.ID), t, t)
}

func (c *Client) AddNote(ctx context.Context, taskID string, n *contracts.Note) error {
	return c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/notes", n, n)
}

// Cycles returns the session's dependency cycles — legal at rest, they block
// pipeline launch (spec §3.6).
func (c *Client) Cycles(ctx context.Context, sessionID string) ([][]string, error) {
	var wrapper struct {
		Cycles [][]string `json:"cycles"`
	}
	err := c.do(ctx, http.MethodGet, "/tasks/cycles?session_id="+url.QueryEscape(sessionID), nil, &wrapper)
	return wrapper.Cycles, err
}

// --- Leases (spec §15, the parking meter) ---

// Claim takes or renews the task's lease for this client's actor.
// ErrConflict when someone else holds it.
func (c *Client) Claim(ctx context.Context, taskID string, ttl time.Duration) error {
	body := map[string]any{}
	if ttl > 0 {
		body["ttl_seconds"] = int(ttl.Seconds())
	}
	return c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/claim", body, nil)
}

func (c *Client) Release(ctx context.Context, taskID string) error {
	return c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/release", map[string]any{}, nil)
}

// ReleaseAll is the supervision call: free everything a dead holder had.
func (c *Client) ReleaseAll(ctx context.Context, holder, reason string) error {
	return c.do(ctx, http.MethodPost, "/leases/release-all",
		map[string]any{"holder": holder, "reason": reason}, nil)
}

// --- Sessions (spec §1) ---

func (c *Client) CreateSession(ctx context.Context, s *contracts.Session) error {
	return c.do(ctx, http.MethodPost, "/sessions", s, s)
}

func (c *Client) Session(ctx context.Context, id string) (contracts.Session, error) {
	var s contracts.Session
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(id), nil, &s)
	return s, err
}

// --- Registries (spec §9, §12, §17) ---

func (c *Client) Agent(ctx context.Context, idOrName string) (contracts.Agent, error) {
	var a contracts.Agent
	err := c.do(ctx, http.MethodGet, "/agents/"+url.PathEscape(idOrName), nil, &a)
	return a, err
}

// --- Templates (spec §6) ---

// Template fetches by id or name; used_by is dropped — callers wanting
// binding info talk to the endpoint directly.
func (c *Client) Template(ctx context.Context, idOrName string) (contracts.Template, error) {
	var wrapper struct {
		Template contracts.Template `json:"template"`
	}
	err := c.do(ctx, http.MethodGet, "/templates/"+url.PathEscape(idOrName), nil, &wrapper)
	return wrapper.Template, err
}

// --- Events (spec §8, §15) ---

// Append posts one standalone event (envelope minus seq/timestamp); the
// client's actor fills the envelope unless the caller set one.
func (c *Client) Append(ctx context.Context, env *contracts.Envelope) error {
	if env.Actor == "" {
		env.Actor = c.actor
	}
	return c.do(ctx, http.MethodPost, "/events", env, env)
}

// Stream opens a filtered SSE subscription (history from the cursor, then
// live pushes). Envelopes arrive on the returned channel until ctx ends or
// the connection drops; the channel then closes. Callers resume by passing
// the last envelope's Seq as since — reconnection policy is theirs.
func (c *Client) Stream(ctx context.Context, since int64, sessionID string, types []string) (<-chan contracts.Envelope, error) {
	q := url.Values{}
	q.Set("since", fmt.Sprint(since))
	if sessionID != "" {
		q.Set("session_id", sessionID)
	}
	if len(types) > 0 {
		q.Set("types", strings.Join(types, ","))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/events?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{}).Do(req) // no timeout: the stream lives until ctx ends
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("stream: status %d", resp.StatusCode)
	}
	ch := make(chan contracts.Envelope, 64)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var env contracts.Envelope
			if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &env) != nil {
				continue
			}
			select {
			case ch <- env:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// Events fetches one filtered batch after the cursor (capped server-side;
// page with since = last seq).
func (c *Client) Events(ctx context.Context, since int64, sessionID string, types []string) ([]contracts.Envelope, error) {
	q := url.Values{}
	q.Set("since", fmt.Sprint(since))
	if sessionID != "" {
		q.Set("session_id", sessionID)
	}
	if len(types) > 0 {
		q.Set("types", strings.Join(types, ","))
	}
	var out []contracts.Envelope
	err := c.do(ctx, http.MethodGet, "/events?"+q.Encode(), nil, &out)
	return out, err
}
