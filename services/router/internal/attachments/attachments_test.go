package attachments

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{R: 255, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sessionOwner(t *testing.T, id string) Owner {
	t.Helper()
	o, err := SessionOwner(id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestSaveImageWritesFileAndThumbnail(t *testing.T) {
	s := New(t.TempDir())
	owner := sessionOwner(t, "ses_1")
	a, err := s.Save(owner, "Screenshot 2026-10-06 at 2.30.12 PM.png", bytes.NewReader(pngBytes(t, 1920, 1080)))
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "Screenshot 2026-10-06 at 2.30.12 PM.png" || !a.Thumbnail || !filepath.IsAbs(a.Path) {
		t.Fatalf("got %+v", a)
	}
	thumb, err := s.ThumbnailPath(owner, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(thumb)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != thumbMaxEdge || cfg.Height != 180 {
		t.Fatalf("thumbnail %dx%d, want %dx180", cfg.Width, cfg.Height, thumbMaxEdge)
	}

	got, err := s.Get(owner, a.ID)
	if err != nil || got != a {
		t.Fatalf("Get = %+v, %v; want %+v", got, err, a)
	}
}

func TestSaveNonImageHasNoThumbnail(t *testing.T) {
	s := New(t.TempDir())
	owner := sessionOwner(t, "ses_1")
	a, err := s.Save(owner, "build.log", strings.NewReader("line one\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Thumbnail || a.Size != 9 {
		t.Fatalf("got %+v", a)
	}
	if _, err := s.ThumbnailPath(owner, a.ID); err != ErrNotFound {
		t.Fatalf("ThumbnailPath err = %v, want ErrNotFound", err)
	}
}

func TestSameNameTwiceKeepsBoth(t *testing.T) {
	s := New(t.TempDir())
	owner := sessionOwner(t, "ses_1")
	a, _ := s.Save(owner, "image.png", strings.NewReader("a"))
	b, _ := s.Save(owner, "image.png", strings.NewReader("b"))
	if a.Path == b.Path {
		t.Fatalf("both saved to %s", a.Path)
	}
}

func TestCleanNameNeverEscapesItsDirectory(t *testing.T) {
	s := New(t.TempDir())
	owner := sessionOwner(t, "ses_1")
	for _, name := range []string{"../../etc/passwd", `..\..\x`, "", ".hidden", "a\nb"} {
		a, err := s.Save(owner, name, strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(a.Path) != filepath.Join(s.root, "ses_1", a.ID) || strings.HasPrefix(a.Name, ".") {
			t.Fatalf("%q saved as %s", name, a.Path)
		}
	}
}

func TestOwnersRejectPathLikeIDs(t *testing.T) {
	for _, id := range []string{"", "..", "a/b", "../x", "a.b"} {
		if _, err := SessionOwner(id); err == nil {
			t.Errorf("SessionOwner(%q) accepted", id)
		}
		if _, err := DraftOwner(id); err == nil {
			t.Errorf("DraftOwner(%q) accepted", id)
		}
	}
	s := New(t.TempDir())
	if _, err := s.Get(sessionOwner(t, "ses_1"), "../x"); err != ErrNotFound {
		t.Fatalf("Get with a path-like id: %v", err)
	}
}

func TestRemoveAndRemoveOwner(t *testing.T) {
	s := New(t.TempDir())
	owner := sessionOwner(t, "ses_1")
	a, _ := s.Save(owner, "a.png", bytes.NewReader(pngBytes(t, 10, 10)))
	b, _ := s.Save(owner, "b.txt", strings.NewReader("b"))
	if err := s.Remove(owner, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(owner, a.ID); err != ErrNotFound {
		t.Fatalf("a still there: %v", err)
	}
	if _, err := s.ThumbnailPath(owner, a.ID); err != ErrNotFound {
		t.Fatalf("a's thumbnail still there: %v", err)
	}
	if _, err := s.Get(owner, b.ID); err != nil {
		t.Fatalf("b went too: %v", err)
	}
	if err := s.RemoveOwner(owner); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "ses_1")); !os.IsNotExist(err) {
		t.Fatalf("session dir survives RemoveOwner: %v", err)
	}
}

func TestAdoptMovesDraftToSession(t *testing.T) {
	s := New(t.TempDir())
	draft, _ := DraftOwner("draft_1")
	a, _ := s.Save(draft, "a.png", bytes.NewReader(pngBytes(t, 10, 10)))
	sess := sessionOwner(t, "ses_1")
	if err := s.Adopt(draft, sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(sess, a.ID)
	if err != nil || !got.Thumbnail || !strings.Contains(got.Path, filepath.Join("ses_1", a.ID)) {
		t.Fatalf("after adopt: %+v, %v", got, err)
	}
	if _, err := s.Get(draft, a.ID); err != ErrNotFound {
		t.Fatalf("still in the draft: %v", err)
	}
	// A draft that never had an attachment adopts as a no-op.
	empty, _ := DraftOwner("draft_2")
	if err := s.Adopt(empty, sessionOwner(t, "ses_2")); err != nil {
		t.Fatal(err)
	}
}

func TestSweepDraftsRemovesOnlyStaleOnes(t *testing.T) {
	s := New(t.TempDir())
	stale, _ := DraftOwner("stale")
	fresh, _ := DraftOwner("fresh")
	a, _ := s.Save(stale, "a.txt", strings.NewReader("a"))
	b, _ := s.Save(fresh, "b.txt", strings.NewReader("b"))
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(s.root, draftsDir, "stale"), old, old); err != nil {
		t.Fatal(err)
	}
	s.SweepDrafts(7 * 24 * time.Hour)
	if _, err := s.Get(stale, a.ID); err != ErrNotFound {
		t.Fatalf("stale draft survived: %v", err)
	}
	if _, err := s.Get(fresh, b.ID); err != nil {
		t.Fatalf("fresh draft swept: %v", err)
	}
}

func TestAppendToMessage(t *testing.T) {
	files := []Attachment{{Path: "/h/.mads/attachments/s/1/a.png"}, {Path: "/h/.mads/attachments/s/2/b c.log"}}
	got := AppendToMessage("why is this red?\n", files)
	want := "why is this red?\n\n" + MessageHeader + "\n- /h/.mads/attachments/s/1/a.png\n- /h/.mads/attachments/s/2/b c.log"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if got := AppendToMessage("  ", files[:1]); got != MessageHeader+"\n- /h/.mads/attachments/s/1/a.png" {
		t.Fatalf("attachments-only message: %q", got)
	}
	if got := AppendToMessage("plain", nil); got != "plain" {
		t.Fatalf("no attachments: %q", got)
	}
}
