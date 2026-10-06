package api_test

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/router/internal/attachments"
)

type uploaded struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Thumbnail bool   `json:"thumbnail"`
}

func uploadAttachment(t *testing.T, url, name string, body []byte) uploaded {
	t.Helper()
	resp, err := http.Post(url+"?name="+name, "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", resp.StatusCode)
	}
	return decodeJSON[uploaded](t, resp)
}

func smallPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 40, 30))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// The launchpad's whole journey: attach to a draft before the session
// exists, start the session from that draft, send the opening message
// naming the attachment, see its path reach the agent — then delete the
// session and find its files gone with it.
func TestDraftAttachmentFollowsItsSessionToTheAgentAndToDeletion(t *testing.T) {
	srv, _ := newTestServerWithRealSession(t)

	a := uploadAttachment(t, srv.URL+"/attachment-drafts/draft_1/attachments", "screen.png", smallPNG(t))
	if !a.Thumbnail || a.Name != "screen.png" {
		t.Fatalf("uploaded %+v", a)
	}
	if code := getStatus(t, srv.URL+"/attachment-drafts/draft_1/attachments/"+a.ID+"/thumbnail"); code != http.StatusOK {
		t.Fatalf("draft thumbnail status = %d", code)
	}

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "with attachments", "provider": "fake", "attachment_draft": "draft_1"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	id := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp).Session.ID

	base := srv.URL + "/sessions/" + id + "/attachments/" + a.ID
	if code := getStatus(t, base+"/thumbnail"); code != http.StatusOK {
		t.Fatalf("adopted thumbnail status = %d", code)
	}
	if code := getStatus(t, base); code != http.StatusOK {
		t.Fatalf("adopted original status = %d", code)
	}

	mresp := postJSON(t, srv.URL+"/sessions/"+id+"/messages", map[string]any{"text": "what's wrong here?", "attachments": []string{a.ID}})
	if mresp.StatusCode != http.StatusAccepted {
		t.Fatalf("send status = %d, want 202", mresp.StatusCode)
	}
	var msgs []contracts.TranscriptMessage
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		tresp, err := http.Get(srv.URL + "/sessions/" + id + "/transcript")
		if err != nil {
			t.Fatal(err)
		}
		if msgs = decodeJSON[[]contracts.TranscriptMessage](t, tresp); len(msgs) > 0 {
			break
		}
	}
	if len(msgs) == 0 || msgs[0].Role != "user" {
		t.Fatalf("transcript = %+v", msgs)
	}
	adopted := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(a.Path)))), id, a.ID, "screen.png")
	want := "what's wrong here?\n\n" + attachments.MessageHeader + "\n- " + adopted
	if msgs[0].Text != want {
		t.Fatalf("user message = %q\nwant %q", msgs[0].Text, want)
	}

	if dresp := httpDelete(t, srv.URL+"/sessions/"+id); dresp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", dresp.StatusCode)
	}
	if _, err := os.Stat(adopted); !os.IsNotExist(err) {
		t.Fatalf("attachment survives its session's delete: %v", err)
	}
}

// A send naming an attachment that doesn't exist is refused outright — the
// agent never gets a message whose file list silently lost an entry.
func TestMessageNamingAMissingAttachmentIsRefused(t *testing.T) {
	srv, base := newTestServerWithRealSession(t)
	id := createSessionDirect(t, base, contracts.Session{Name: "no such file", Provider: "fake"})

	resp := postJSON(t, srv.URL+"/sessions/"+id+"/messages", map[string]any{"text": "hi", "attachments": []string{"deadbeef"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUploadToUnknownSessionIs404(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Post(srv.URL+"/sessions/nope/attachments?name=a.txt", "text/plain", bytes.NewReader([]byte("a")))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRemovedAttachmentIsGone(t *testing.T) {
	srv := newTestServer(t)
	a := uploadAttachment(t, srv.URL+"/attachment-drafts/d/attachments", "notes.txt", []byte("hello"))
	if a.Thumbnail {
		t.Fatal("a text file got a thumbnail")
	}
	url := srv.URL + "/attachment-drafts/d/attachments/" + a.ID
	if code := getStatus(t, url); code != http.StatusOK {
		t.Fatalf("get status = %d", code)
	}
	if resp := httpDelete(t, url); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	if code := getStatus(t, url); code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404", code)
	}
}
