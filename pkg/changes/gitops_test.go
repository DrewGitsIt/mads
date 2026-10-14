package changes

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The temp index must keep the shadow index's mtime: a copy stamped "now"
// defeats git's racy-clean check, and a same-size edit made in the same
// second as the last turn commit reads as unchanged (seen as flaky
// TestBuild* failures before the fix).
func TestCopyFileKeepsMtime(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "index"), filepath.Join(dir, "copy")
	if err := os.WriteFile(src, []byte("idx"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(src, old, old); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(old) {
		t.Errorf("copy mtime = %v, want %v", fi.ModTime(), old)
	}
	if raw, _ := os.ReadFile(dst); string(raw) != "idx" { //nolint:gosec // test temp file
		t.Errorf("copy content = %q", raw)
	}
}

func TestParseNumstatZ(t *testing.T) {
	got := parseNumstatZ([]byte("3\t1\ta.go\x00-\t-\tb.bin\x00"))
	if e := got["a.go"]; e.Added != 3 || e.Deleted != 1 || e.Binary {
		t.Errorf("a.go = %+v", e)
	}
	if e := got["b.bin"]; !e.Binary {
		t.Errorf("b.bin = %+v, want binary", e)
	}
	names := parseNameStatusZ([]byte("A\x00n.go\x00D\x00d.go\x00M\x00m.go\x00T\x00t.go\x00"))
	want := map[string]string{"n.go": "added", "d.go": "deleted", "m.go": "modified", "t.go": "modified"}
	for k, v := range want {
		if names[k] != v {
			t.Errorf("%s = %q, want %q", k, names[k], v)
		}
	}
}
