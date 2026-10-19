package orchestratemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// linkedSession is one row of the Router's subjects route
// (GET /sessions/{id}/orchestrate/subjects), the table every per-session
// tool resolves its `session` argument against.
type linkedSession struct {
	LinkID           string   `json:"link_id"`
	SubjectSessionID string   `json:"subject_session_id"`
	SubjectName      string   `json:"subject_name"`
	Grants           []string `json:"grants"`
	Activity         string   `json:"activity"`
	Cadence          string   `json:"cadence"`
}

// listedSession is list_linked_sessions' row as the agent sees it: the name
// it passes back as `session`, with no link ID — the tools take names, so a
// link ID would only be one more identifier to confuse with the session's.
type listedSession struct {
	Name      string   `json:"name"`
	SessionID string   `json:"session_id"`
	Grants    []string `json:"grants"`
	Activity  string   `json:"activity,omitempty"`
	Cadence   string   `json:"cadence,omitempty"`
}

// toolError is a failure the agent can act on — an unknown or ambiguous
// session — returned as an isError tool result rather than a JSON-RPC
// error, so the harness hands its text to the model instead of treating
// the call as broken.
type toolError struct{ msg string }

func (e *toolError) Error() string { return e.msg }

// linkedSessions fetches the caller's linked sessions. A Router error
// status comes back as a toolError carrying the Router's own body, the same
// text the tool would have shown had it called the route directly.
func (c *client) linkedSessions(ctx context.Context) ([]linkedSession, error) {
	status, body, err := c.listLinks(ctx)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, &toolError{msg: string(body)}
	}
	var out []linkedSession
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode linked sessions: %w", err)
	}
	return out, nil
}

// resolveSession maps a tool's `session` argument to one linked session:
// exact name first, then exact session ID, then a case-insensitive name.
// Looked up fresh on every call, never cached: sessions get renamed (auto-
// naming runs after the first turns) and links come and go while this
// server runs (2026-10-19). Two different sessions sharing the matched name
// is ambiguous; the error lists their IDs, which always resolve.
func resolveSession(sessions []linkedSession, arg string) (linkedSession, error) {
	if strings.TrimSpace(arg) == "" {
		return linkedSession{}, &toolError{msg: "session is required. " + linkedNames(sessions)}
	}
	for _, match := range []func(linkedSession) bool{
		func(s linkedSession) bool { return s.SubjectName == arg },
		func(s linkedSession) bool { return s.SubjectSessionID == arg },
		func(s linkedSession) bool { return strings.EqualFold(s.SubjectName, arg) },
	} {
		var hits []linkedSession
		for _, s := range sessions {
			if match(s) && !containsSession(hits, s.SubjectSessionID) {
				hits = append(hits, s)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		default:
			parts := make([]string, len(hits))
			for i, h := range hits {
				parts[i] = fmt.Sprintf("%q (%s)", h.SubjectName, h.SubjectSessionID)
			}
			return linkedSession{}, &toolError{msg: fmt.Sprintf(
				"More than one linked session is named %q: %s. Pass the session ID instead.", arg, strings.Join(parts, ", "))}
		}
	}
	return linkedSession{}, &toolError{msg: fmt.Sprintf("No linked session is named %q. %s", arg, linkedNames(sessions))}
}

func containsSession(list []linkedSession, sessionID string) bool {
	for _, s := range list {
		if s.SubjectSessionID == sessionID {
			return true
		}
	}
	return false
}

// linkedNames is the "here's what you can pass" half of a resolution error.
// A name two sessions share carries each one's ID, since the bare name
// wouldn't resolve.
func linkedNames(sessions []linkedSession) string {
	if len(sessions) == 0 {
		return "You have no linked sessions right now."
	}
	count := map[string]int{}
	seen := map[string]bool{}
	var unique []linkedSession
	for _, s := range sessions {
		if seen[s.SubjectSessionID] {
			continue
		}
		seen[s.SubjectSessionID] = true
		unique = append(unique, s)
		count[s.SubjectName]++
	}
	parts := make([]string, len(unique))
	for i, s := range unique {
		if count[s.SubjectName] > 1 {
			parts[i] = fmt.Sprintf("%q (%s)", s.SubjectName, s.SubjectSessionID)
		} else {
			parts[i] = fmt.Sprintf("%q", s.SubjectName)
		}
	}
	return "Linked sessions: " + strings.Join(parts, ", ") + "."
}
