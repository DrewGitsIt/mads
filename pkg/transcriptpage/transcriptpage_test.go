package transcriptpage

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// rows builds a transcript from "role:key" specs.
func rows(specs ...string) []contracts.TranscriptMessage {
	out := make([]contracts.TranscriptMessage, len(specs))
	for i, s := range specs {
		role, key, _ := strings.Cut(s, ":")
		out[i] = contracts.TranscriptMessage{Role: role, Key: key, Text: s}
	}
	return out
}

func keys(ms []contracts.TranscriptMessage) string {
	ks := make([]string, len(ms))
	for i, m := range ms {
		ks[i] = m.Role + ":" + m.Key
	}
	return strings.Join(ks, " ")
}

// u1 a1 t1 r1 a2 u2 a3: a tool call (tool:t1) and its result (tool_result:t1)
// share a key.
var conv = rows("user:u1", "agent:a1", "tool:t1", "tool_result:t1", "agent:a2", "user:u2", "agent:a3")

func TestPage(t *testing.T) {
	cases := []struct {
		name    string
		in      []contracts.TranscriptMessage
		q       Query
		want    string
		hasMore bool
	}{
		{"last page", conv, Query{Limit: 2}, "user:u2 agent:a3", true},
		{"middle page", conv, Query{Before: "a2", HasBefore: true, Limit: 2}, "tool:t1 tool_result:t1", true},
		// The window would start on the result; it grows back to the call.
		{"pair straddling the boundary", conv, Query{Before: "a2", HasBefore: true, Limit: 1},
			"tool:t1 tool_result:t1", true},
		{"first page", conv, Query{Before: "t1", HasBefore: true, Limit: 5}, "user:u1 agent:a1", false},
		{"exactly the rest", conv, Query{Before: "t1", HasBefore: true, Limit: 2}, "user:u1 agent:a1", false},
		{"before the first row", conv, Query{Before: "u1", HasBefore: true, Limit: 3}, "", false},
		{"before without limit", conv, Query{Before: "a2", HasBefore: true}, "user:u1 agent:a1 tool:t1 tool_result:t1", false},
		{"limit beyond length", conv, Query{Limit: 50}, keys(conv), false},
		{"empty transcript", nil, Query{Limit: 3}, "", false},
		// Parallel calls interleave: the result of the first pulls in both calls.
		{"interleaved calls", rows("user:u", "tool:x", "tool:y", "tool_result:x", "tool_result:y"), Query{Limit: 2},
			"tool:x tool:y tool_result:x tool_result:y", true},
		{"unkeyed, limit by position", rows("user:", "agent:", "agent:"), Query{Limit: 2}, "agent: agent:", true},
		// linkwrap.SegmentKey's sub-keys name each segment of a split send.
		{"before a sub-key", rows("user:u1", "user:u2#0", "user:u2#1", "agent:a"), Query{Before: "u2#1", HasBefore: true, Limit: 1},
			"user:u2#0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Page(tc.in, tc.q)
			if err != nil {
				t.Fatal(err)
			}
			if got := keys(p.Rows); got != tc.want || p.HasMore != tc.hasMore {
				t.Errorf("Page = %q has_more=%v, want %q has_more=%v", got, p.HasMore, tc.want, tc.hasMore)
			}
			if p.Rows == nil {
				t.Error("Rows is nil; it must encode as []")
			}
		})
	}
}

// Paging backwards by Rows[0].Key visits every row exactly once, whatever
// the page size, and never splits a call from its result.
func TestPageWalkCoversEveryRowOnce(t *testing.T) {
	in := rows("user:u1", "tool:x", "tool:y", "tool_result:x", "agent:a1", "tool_result:y",
		"user:u2#0", "user:u2#1", "agent:a2", "tool:z", "tool_result:z", "agent:a3")
	for limit := 1; limit <= len(in)+1; limit++ {
		var got []contracts.TranscriptMessage
		q := Query{Limit: limit}
		for {
			p, err := Page(in, q)
			if err != nil {
				t.Fatalf("limit %d: %v", limit, err)
			}
			for i, m := range p.Rows {
				if m.Role == "tool_result" && (i == 0 || !hasCall(p.Rows[:i], m.Key)) {
					t.Errorf("limit %d: result %s without its call on the page %q", limit, m.Key, keys(p.Rows))
				}
			}
			got = append(append([]contracts.TranscriptMessage{}, p.Rows...), got...)
			if !p.HasMore {
				break
			}
			q = Query{Before: p.Rows[0].Key, HasBefore: true, Limit: limit}
		}
		if !reflect.DeepEqual(got, in) {
			t.Errorf("limit %d: walk = %q, want %q", limit, keys(got), keys(in))
		}
	}
}

func hasCall(ms []contracts.TranscriptMessage, key string) bool {
	for _, m := range ms {
		if m.Role == "tool" && m.Key == key {
			return true
		}
	}
	return false
}

func TestPageErrors(t *testing.T) {
	if _, err := Page(conv, Query{Before: "nope", HasBefore: true, Limit: 2}); err != ErrUnknownKey {
		t.Errorf("unknown key: err = %v", err)
	}
	if _, err := Page(rows("user:u1", "agent:"), Query{Before: "u1", HasBefore: true}); err != ErrUnkeyed {
		t.Errorf("unkeyed: err = %v", err)
	}
}

func TestRespond(t *testing.T) {
	cases := []struct {
		query  string
		status int
		code   string
	}{
		{"", http.StatusOK, ""},
		{"page=2", http.StatusOK, ""},
		{"limit=2", http.StatusOK, ""},
		{"limit=0", http.StatusBadRequest, contracts.TranscriptErrBadParam},
		{"limit=x", http.StatusBadRequest, contracts.TranscriptErrBadParam},
		{"before=", http.StatusBadRequest, contracts.TranscriptErrBadParam},
		{"before=nope&limit=2", http.StatusGone, contracts.TranscriptErrUnknownKey},
	}
	for _, tc := range cases {
		q, _ := url.ParseQuery(tc.query)
		status, body := Respond(q, conv)
		if status != tc.status {
			t.Errorf("%q: status %d, want %d", tc.query, status, tc.status)
		}
		switch b := body.(type) {
		case []contracts.TranscriptMessage:
			if tc.query == "limit=2" || tc.status != http.StatusOK {
				t.Errorf("%q: bare array, want an envelope or error", tc.query)
			}
		case contracts.TranscriptPage:
			if tc.query != "limit=2" {
				t.Errorf("%q: envelope, want the bare array", tc.query)
			}
		case map[string]string:
			if b["code"] != tc.code || b["error"] == "" {
				t.Errorf("%q: error body %v, want code %q", tc.query, b, tc.code)
			}
		}
	}
	q, _ := url.ParseQuery("before=u1")
	if status, body := Respond(q, rows("user:u1", "agent:")); status != http.StatusBadRequest ||
		body.(map[string]string)["code"] != contracts.TranscriptErrUnkeyed {
		t.Errorf("unkeyed before: %d %v", status, body)
	}
}
