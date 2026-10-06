// Package attachments stores the files a user pastes, drops or picks into a
// composer, so the agent can open them by path. Nothing is ever inlined into
// a message: the message carries each file's absolute path (AppendToMessage)
// and the agent reads it with whatever tool it has — how a harness looks at
// an image is the harness's business, and it gets to keep improving at it.
//
// Layout, under $MADS_HOME/attachments:
//
//	<session id>/<attachment id>/<original name>   the file, as uploaded
//	<session id>/.thumbs/<attachment id>.png        a small preview, images only
//	drafts/<draft id>/...                           the same, for a launchpad
//	                                                goal whose session doesn't
//	                                                exist yet (see Adopt)
//
// A session's directory goes when the session is deleted (RemoveOwner).
package attachments

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // registers the GIF decoder for thumbnails
	_ "image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// ErrNotFound is returned for an attachment id with nothing on disk behind it.
var ErrNotFound = errors.New("attachment not found")

// thumbMaxEdge bounds a thumbnail's longer side, in pixels — about 2x the
// chip and transcript-bubble size, so it stays sharp on a retina display.
const thumbMaxEdge = 320

// thumbMaxPixels skips thumbnailing anything larger: decoding holds the
// whole bitmap in memory (4 bytes a pixel), so a hostile or freak image
// can't make the Router allocate gigabytes. The file itself is still kept.
const thumbMaxPixels = 64 << 20

const thumbsDir = ".thumbs"
const draftsDir = "drafts"

// Attachment is one stored file.
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Path is absolute — what the agent is handed to open.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Thumbnail reports whether a preview exists (the file decoded as an image).
	Thumbnail bool `json:"thumbnail"`
}

// Owner names whose files these are: a session, or a launchpad draft.
type Owner struct {
	dir string // relative to the store root
}

// validID guards every path component a client supplies: session, draft and
// attachment ids are all opaque tokens, never anything that could climb out
// of the store (no separators, no dots).
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// SessionOwner is the owner for a session's attachments.
func SessionOwner(sessionID string) (Owner, error) {
	if !validID.MatchString(sessionID) {
		return Owner{}, fmt.Errorf("invalid session id %q", sessionID)
	}
	return Owner{dir: sessionID}, nil
}

// DraftOwner is the owner for a launchpad draft's attachments.
func DraftOwner(draftID string) (Owner, error) {
	if !validID.MatchString(draftID) {
		return Owner{}, fmt.Errorf("invalid draft id %q", draftID)
	}
	return Owner{dir: filepath.Join(draftsDir, draftID)}, nil
}

// Store is the attachments directory.
type Store struct {
	root string
}

// New returns a Store rooted at root (normally $MADS_HOME/attachments).
// Nothing is created until the first Save.
func New(root string) *Store {
	return &Store{root: root}
}

// Save streams r to disk under owner as name and, when it decodes as an
// image, writes a thumbnail beside it. There is deliberately no size cap:
// the agent decides how to read what it's handed, and the bytes never pass
// through memory here.
func (s *Store) Save(owner Owner, name string, r io.Reader) (Attachment, error) {
	id, err := newID()
	if err != nil {
		return Attachment{}, err
	}
	dir := filepath.Join(s.root, owner.dir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Attachment{}, err
	}
	path := filepath.Join(dir, cleanName(name))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Attachment{}, err
	}
	size, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = os.RemoveAll(dir)
		return Attachment{}, err
	}
	thumb := s.writeThumbnail(owner, id, path) == nil
	return Attachment{ID: id, Name: filepath.Base(path), Path: path, Size: size, Thumbnail: thumb}, nil
}

// Get looks up an attachment by id.
func (s *Store) Get(owner Owner, id string) (Attachment, error) {
	if !validID.MatchString(id) {
		return Attachment{}, ErrNotFound
	}
	dir := filepath.Join(s.root, owner.dir, id)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].IsDir() {
		return Attachment{}, ErrNotFound
	}
	info, err := entries[0].Info()
	if err != nil {
		return Attachment{}, ErrNotFound
	}
	_, thumbErr := os.Stat(s.thumbPath(owner, id))
	return Attachment{
		ID:        id,
		Name:      info.Name(),
		Path:      filepath.Join(dir, info.Name()),
		Size:      info.Size(),
		Thumbnail: thumbErr == nil,
	}, nil
}

// ThumbnailPath is the preview's path, or ErrNotFound when there is none.
func (s *Store) ThumbnailPath(owner Owner, id string) (string, error) {
	if !validID.MatchString(id) {
		return "", ErrNotFound
	}
	p := s.thumbPath(owner, id)
	if _, err := os.Stat(p); err != nil {
		return "", ErrNotFound
	}
	return p, nil
}

// Remove deletes one attachment — a chip removed before its message went
// out. Removing one that's already gone is not an error.
func (s *Store) Remove(owner Owner, id string) error {
	if !validID.MatchString(id) {
		return ErrNotFound
	}
	_ = os.Remove(s.thumbPath(owner, id))
	return os.RemoveAll(filepath.Join(s.root, owner.dir, id))
}

// RemoveOwner deletes everything owner has — its session was deleted, or a
// draft was abandoned.
func (s *Store) RemoveOwner(owner Owner) error {
	return os.RemoveAll(filepath.Join(s.root, owner.dir))
}

// Adopt moves a launchpad draft's attachments to the session it just
// started, so they live (and are deleted) with that session. A draft with
// no attachments is a no-op. The new session's directory can't exist yet,
// so this is a single rename.
func (s *Store) Adopt(draft Owner, session Owner) error {
	from := filepath.Join(s.root, draft.dir)
	if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	to := filepath.Join(s.root, session.dir)
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	return os.Rename(from, to)
}

// SweepDrafts removes drafts untouched for longer than maxAge — a goal that
// was attached to and then never started (or a draft lost to a reload).
func (s *Store) SweepDrafts(maxAge time.Duration) {
	dir := filepath.Join(s.root, draftsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

func (s *Store) thumbPath(owner Owner, id string) string {
	return filepath.Join(s.root, owner.dir, thumbsDir, id+".png")
}

// writeThumbnail scales an image down to thumbMaxEdge and saves it as PNG.
// Anything that doesn't decode as an image (or is too large to decode
// safely) just has no thumbnail — the chip shows a file glyph instead.
func (s *Store) writeThumbnail(owner Owner, id, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > thumbMaxPixels {
		return errors.New("image too large to thumbnail")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return err
	}
	w, h := fit(cfg.Width, cfg.Height, thumbMaxEdge)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)

	out := s.thumbPath(owner, id)
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	tf, err := os.Create(out)
	if err != nil {
		return err
	}
	if err := errors.Join(png.Encode(tf, dst), tf.Close()); err != nil {
		_ = os.Remove(out)
		return err
	}
	return nil
}

// fit scales w×h so its longer side is at most maxEdge, never upscaling.
func fit(w, h, maxEdge int) (int, int) {
	if w <= maxEdge && h <= maxEdge {
		return w, h
	}
	if w >= h {
		return maxEdge, max(1, h*maxEdge/w)
	}
	return max(1, w*maxEdge/h), maxEdge
}

// cleanName keeps a client-supplied filename to one safe path component:
// the agent sees it (it's in the path), so it stays as recognisable as
// possible, minus anything that could act as a separator or hide.
func cleanName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimLeft(strings.TrimSpace(name), ".")
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = strings.ToValidUTF8(name[:200-len(ext)], "") + ext
	}
	if name == "" {
		return "attachment"
	}
	return name
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
