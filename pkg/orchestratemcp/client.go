package orchestratemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// client is a purpose-built HTTP client for the Router's three orchestrate
// routes — deliberately not pkg/dataclient, which is scoped to the Data
// Service (its own doc comment says so) and this tool talks to the Router,
// never the Data Service directly. Every request is actor-stamped
// X-Mads-Actor: session-<sessionID>, mirroring dataclient's own convention
// (pkg/dataclient/dataclient.go) so Router-side logging/attribution stays
// consistent across both clients.
type client struct {
	baseURL   string
	sessionID string
	http      *http.Client
}

func newClient(baseURL, sessionID string) *client {
	return &client{
		baseURL:   strings.TrimSuffix(baseURL, "/"),
		sessionID: sessionID,
		http:      &http.Client{Timeout: 35 * time.Second},
	}
}

func (c *client) do(ctx context.Context, method, path string, in any) (status int, body []byte, err error) {
	var reqBody io.Reader
	if in != nil {
		raw, marshalErr := json.Marshal(in)
		if marshalErr != nil {
			return 0, nil, marshalErr
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return 0, nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Mads-Actor", "session-"+c.sessionID)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

func (c *client) listLinks(ctx context.Context) (int, []byte, error) {
	return c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(c.sessionID)+"/orchestrate/subjects", nil)
}

func (c *client) getSummary(ctx context.Context, subjectSessionID string) (int, []byte, error) {
	path := "/sessions/" + url.PathEscape(c.sessionID) + "/orchestrate/summary?for=" + url.QueryEscape(subjectSessionID)
	return c.do(ctx, http.MethodGet, path, nil)
}

func (c *client) sendMessage(ctx context.Context, linkID, text string, waitMS int64) (int, []byte, error) {
	body := map[string]any{"text": text}
	if waitMS > 0 {
		body["wait_ms"] = waitMS
	}
	return c.do(ctx, http.MethodPost, "/links/"+url.PathEscape(linkID)+"/orchestrate/message", body)
}
