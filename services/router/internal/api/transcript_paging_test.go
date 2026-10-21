package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/linkwrap"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// pagingPrompts is one conversation: echo hi (user, agent), tool-text (user,
// agent, tool, tool_result, agent), echo bye (user, agent) — 9 rows, with a
// call and its result sharing a key.
var pagingPrompts = []string{"echo hi", "tool-text", "echo bye"}

// driveTurns plays prompts through a session of fp, each to its turn end.
func driveTurns(t *testing.T, fp *fake.Fake, prompts []string) string {
	t.Helper()
	ctx := context.Background()
	sess, err := fp.Start(ctx, provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prompts {
		if err := sess.Send(ctx, p); err != nil {
			t.Fatal(err)
		}
		for ev := range sess.Events() {
			if ev.Kind == provider.EventTurnEnd {
				break
			}
		}
	}
	return sess.Ref()
}

func getRaw(t *testing.T, u string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(u) //nolint:gosec,noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

// A dormant session's transcript (the stored-ref fallback) with no paging
// parameter is today's bare array byte for byte, plus each row's key.
func TestSessionTranscriptFallbackLegacyBytes(t *testing.T) {
	fp := fake.New()
	srv, base := newTestServerWithFake(t, fp)
	ref := driveConversation(t, fp)
	id := createSessionDirect(t, base, contracts.Session{Name: "legacy", Provider: "fake", ProviderRef: ref})
	const zero = `"timestamp":"0001-01-01T00:00:00Z"`
	want := fmt.Sprintf(`[{"role":"user","text":"echo hi",%[2]s,"key":"user:%[1]s:t1:user"},`+
		`{"role":"agent","text":"you said hi",%[2]s,"key":"%[1]s:t1:reply"}]`+"\n", ref, zero)
	for _, q := range []string{"", "?page=2"} {
		if status, got := getRaw(t, srv.URL+"/sessions/"+id+"/transcript"+q); status != http.StatusOK || string(got) != want {
			t.Errorf("GET transcript%s = %d\n%s\nwant\n%s", q, status, got, want)
		}
	}
}

// The live child (proxied) and the stored-ref fallback page the same
// conversation the same way: the same windows, has_more and errors at every
// page size, each path's cursor being its own previous page's first key.
func TestSessionTranscriptProxyAndFallbackPageAlike(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{sessionBin, "-data-url", base}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	fp := fake.New()
	reg := provider.NewRegistry()
	if err := reg.Register(fp); err != nil {
		t.Fatal(err)
	}
	opts := testOptions(t, data, sup)
	opts.Providers = reg
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)

	// Live: a real session child (provider fake) behind the Router.
	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "live", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	live := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp).Session.ID
	t.Cleanup(func() { _ = postJSON(t, srv.URL+"/sessions/"+live+"/terminate", nil).Body.Close() })
	for i, p := range pagingPrompts {
		deadline := time.Now().Add(10 * time.Second)
		for {
			r := postJSON(t, srv.URL+"/sessions/"+live+"/messages", map[string]string{"text": p})
			_ = r.Body.Close()
			if r.StatusCode == http.StatusAccepted {
				break
			}
			if r.StatusCode != http.StatusConflict || time.Now().After(deadline) {
				t.Fatalf("send %q: status %d", p, r.StatusCode)
			}
			time.Sleep(20 * time.Millisecond)
		}
		want := []int{2, 7, 9}[i]
		for {
			_, b := getRaw(t, srv.URL+"/sessions/"+live+"/transcript")
			var rows []contracts.TranscriptMessage
			if err := json.Unmarshal(b, &rows); err != nil {
				t.Fatalf("%v: %s", err, b)
			}
			if len(rows) >= want {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("turn %q: %d rows, want %d", p, len(rows), want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Stored: the same conversation played through the Router's own fake,
	// read back from the session's ref with no child.
	stored := createSessionDirect(t, base, contracts.Session{
		Name: "stored", Provider: "fake", ProviderRef: driveTurns(t, fp, pagingPrompts),
	})

	// walk pages one path backwards at limit n, and describes each page
	// without keys or timestamps (which differ between the two sessions).
	walk := func(id string, n int) []string {
		var pages []string
		q := url.Values{"limit": {fmt.Sprint(n)}}
		for {
			status, b := getRaw(t, srv.URL+"/sessions/"+id+"/transcript?"+q.Encode())
			if status != http.StatusOK {
				t.Fatalf("%s ?%s: %d %s", id, q.Encode(), status, b)
			}
			var p contracts.TranscriptPage
			if err := json.Unmarshal(b, &p); err != nil {
				t.Fatalf("%v: %s", err, b)
			}
			desc := make([]string, len(p.Rows))
			for i, m := range p.Rows {
				if m.Key == "" {
					t.Errorf("%s: row without a key: %+v", id, m)
				}
				desc[i] = m.Role + ":" + m.Text
			}
			pages = append(pages, fmt.Sprintf("%s has_more=%v", strings.Join(desc, "|"), p.HasMore))
			if !p.HasMore {
				return pages
			}
			q.Set("before", p.Rows[0].Key)
		}
	}
	for n := 1; n <= 10; n++ {
		l, s := walk(live, n), walk(stored, n)
		if strings.Join(l, "\n") != strings.Join(s, "\n") {
			t.Errorf("limit %d:\nlive:\n%s\nstored:\n%s", n, strings.Join(l, "\n"), strings.Join(s, "\n"))
		}
	}

	for _, q := range []string{"before=nope&limit=2", "limit=0", "before="} {
		ls, lb := getRaw(t, srv.URL+"/sessions/"+live+"/transcript?"+q)
		ss, sb := getRaw(t, srv.URL+"/sessions/"+stored+"/transcript?"+q)
		if ls != ss || !bytes.Equal(lb, sb) || ls < 400 {
			t.Errorf("?%s: live %d %s, stored %d %s; want the same error", q, ls, lb, ss, sb)
		}
	}
}

// A coalesced run of two digests is one stored user row that the transcript
// splits in two: each segment has its own sub-key, and a page can end
// between them.
func TestSessionTranscriptFallbackPagesBySubKey(t *testing.T) {
	fp := fake.New()
	srv, base := newTestServerWithFake(t, fp)
	d := contracts.Attribution{SourceName: "W", Kind: linkwrap.KindDigest}
	ref := driveTurns(t, fp, []string{linkwrap.Wrap("a", d) + "\n\n" + linkwrap.Wrap("b", d)})
	id := createSessionDirect(t, base, contracts.Session{Name: "run", Provider: "fake", ProviderRef: ref})

	status, b := getRaw(t, srv.URL+"/sessions/"+id+"/transcript?before="+url.QueryEscape("user:"+ref+":t1:user#1")+"&limit=5")
	var p contracts.TranscriptPage
	if err := json.Unmarshal(b, &p); err != nil || status != http.StatusOK {
		t.Fatalf("%d %v: %s", status, err, b)
	}
	if len(p.Rows) != 1 || p.Rows[0].Key != "user:"+ref+":t1:user#0" || p.Rows[0].Text != "a" || p.HasMore {
		t.Errorf("page before the second digest = %+v", p)
	}
}
