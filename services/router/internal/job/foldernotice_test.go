package job

import (
	"fmt"
	"strings"
	"testing"
)

// The notice names every file up to a cap, then counts the rest.
func TestFolderNoticeCapsTheList(t *testing.T) {
	var files []string
	for i := 0; i < folderNoticeMaxFiles+3; i++ {
		files = append(files, fmt.Sprintf("f%d.txt", i))
	}
	got := FolderNotice("", files)
	if !strings.HasPrefix(got, "A pipeline task merged into your folder") {
		t.Errorf("notice = %q", got)
	}
	if strings.Contains(got, fmt.Sprintf("f%d.txt", folderNoticeMaxFiles)) || !strings.Contains(got, "and 3 more") {
		t.Errorf("notice didn't cap the list:\n%s", got)
	}
}
