// Package transcriptpage pages GET /transcript by key
// (docs/CHAT-STREAM-DESIGN.md §4). The session child and the Router's
// stored-ref fallback both answer through Respond, so the two paths page
// the same rows the same way. The wire rules are contracts.TranscriptPage's.
package transcriptpage

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Query is a parsed paging request.
type Query struct {
	// Before is the cursor key; meaningful only when HasBefore.
	Before    string
	HasBefore bool
	// Limit is the page size; 0 means no limit.
	Limit int
}

// Errors Page returns; Respond maps them to the contract's status and code.
var (
	ErrUnkeyed    = errors.New("transcript has unkeyed rows, so it can't be paged by key; page with limit alone")
	ErrUnknownKey = errors.New("no transcript row has the before key")
)

// errBadParam wraps a malformed parameter.
type errBadParam struct{ msg string }

func (e errBadParam) Error() string { return e.msg }

// ParseQuery reads ?before and ?limit. paged is false when neither is
// present: the caller answers the legacy bare array then. Other parameters
// (the old, never-implemented ?page) are ignored.
func ParseQuery(q url.Values) (query Query, paged bool, err error) {
	if !q.Has("before") && !q.Has("limit") {
		return Query{}, false, nil
	}
	if q.Has("before") {
		query.Before, query.HasBefore = q.Get("before"), true
		if query.Before == "" {
			return Query{}, true, errBadParam{"before must be a row key"}
		}
	}
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 {
			return Query{}, true, errBadParam{"limit must be a positive integer"}
		}
		query.Limit = n
	}
	return query, true, nil
}

// Page cuts q's page out of rows (the whole transcript, in conversation
// order). The window ends just before the first row keyed q.Before (or at
// the end), starts q.Limit rows earlier, and then grows back to the first
// row of every item it holds, so no item (rows sharing a key: a tool call
// and its result) is split across pages. Rows without a key are items of
// their own.
func Page(rows []contracts.TranscriptMessage, q Query) (contracts.TranscriptPage, error) {
	first := make(map[string]int, len(rows))
	for i, m := range rows {
		if m.Key == "" {
			if q.HasBefore {
				return contracts.TranscriptPage{}, ErrUnkeyed
			}
			continue
		}
		if _, seen := first[m.Key]; !seen {
			first[m.Key] = i
		}
	}
	end := len(rows)
	if q.HasBefore {
		i, ok := first[q.Before]
		if !ok {
			return contracts.TranscriptPage{}, ErrUnknownKey
		}
		end = i
	}
	start := 0
	if q.Limit > 0 && end-q.Limit > 0 {
		start = end - q.Limit
	}
	// One downward pass suffices: start only falls, and the loop re-reads it.
	for j := end - 1; j >= start; j-- {
		if k := rows[j].Key; k != "" && first[k] < start {
			start = first[k]
		}
	}
	page := make([]contracts.TranscriptMessage, end-start)
	copy(page, rows[start:end])
	return contracts.TranscriptPage{Rows: page, HasMore: start > 0}, nil
}

// Respond is a GET /transcript handler's answer for query q over rows: the
// status and the value to encode as JSON. With no paging parameters it is
// rows itself, the legacy bare array, unchanged.
func Respond(q url.Values, rows []contracts.TranscriptMessage) (int, any) {
	query, paged, err := ParseQuery(q)
	if !paged {
		return http.StatusOK, rows
	}
	if err == nil {
		var page contracts.TranscriptPage
		if page, err = Page(rows, query); err == nil {
			return http.StatusOK, page
		}
	}
	return errorResponse(err)
}

func errorResponse(err error) (int, any) {
	status, code := http.StatusBadRequest, contracts.TranscriptErrBadParam
	switch {
	case errors.Is(err, ErrUnkeyed):
		code = contracts.TranscriptErrUnkeyed
	case errors.Is(err, ErrUnknownKey):
		status, code = http.StatusGone, contracts.TranscriptErrUnknownKey
	}
	return status, map[string]string{"error": err.Error(), "code": code}
}
