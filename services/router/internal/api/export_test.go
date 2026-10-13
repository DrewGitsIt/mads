package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
)

func TestSessionExport(t *testing.T) {
	fp := fake.New()
	srv, base := newTestServerWithFake(t, fp)
	ref := driveConversation(t, fp)
	id := createSessionDirect(t, base, contracts.Session{
		Name: "My: export", Provider: "fake", ProviderRef: ref, Activity: contracts.SessionInactive,
	})
	if _, err := dataclient.New(base, "router").PutDelegate(context.Background(), contracts.Delegate{
		SessionID: id, HarnessID: "a1", Description: "Look around", State: contracts.DelegateCompleted, Runs: 1, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/sessions/" + id + "/export?format=json")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !regexp.MustCompile(`^attachment; filename="My-export_\d{4}-\d{2}-\d{2}\.json"$`).MatchString(cd) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	var doc struct {
		Schema  string `json:"schema"`
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Messages  []contracts.TranscriptMessage `json:"messages"`
		Delegates []struct {
			ID       string                        `json:"id"`
			Name     string                        `json:"name"`
			Messages []contracts.TranscriptMessage `json:"messages"`
		} `json:"delegates"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != "mads.export/v1" || doc.Session.ID != id || len(doc.Messages) != 2 {
		t.Errorf("export = %s", body)
	}
	if len(doc.Delegates) != 1 || doc.Delegates[0].ID != "a1" || doc.Delegates[0].Name != "Look around" || doc.Delegates[0].Messages == nil {
		t.Errorf("delegates = %+v", doc.Delegates)
	}

	resp, err = http.Get(srv.URL + "/sessions/" + id + "/export?format=md")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.HasSuffix(resp.Header.Get("Content-Disposition"), `.md"`) || !strings.Contains(string(body), "you said hi") || !strings.Contains(string(body), "## Subagent: Look around") {
		t.Errorf("markdown export: %q\n%s", resp.Header.Get("Content-Disposition"), body)
	}

	resp, err = http.Get(srv.URL + "/sessions/" + id + "/export?format=pdf")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad format: status %d, want 400", resp.StatusCode)
	}
}
