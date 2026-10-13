package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

// TestCompactSendsSlashCommand: Compact is the "/compact" user turn, with
// any instructions riding after it exactly as typed.
func TestCompactSendsSlashCommand(t *testing.T) {
	for _, c := range []struct{ instructions, want string }{
		{"", "/compact"},
		{"   ", "/compact"},
		{" keep the queue work ", "/compact keep the queue work"},
	} {
		var buf bytes.Buffer
		s := &streamSession{stdin: nopWriteCloser{&buf}}
		if err := s.Compact(context.Background(), c.instructions); err != nil {
			t.Fatalf("Compact(%q): %v", c.instructions, err)
		}
		var line struct {
			Message struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
			t.Fatalf("stdin line %q: %v", buf.String(), err)
		}
		if got := line.Message.Content[0].Text; got != c.want {
			t.Errorf("Compact(%q) sent %q, want %q", c.instructions, got, c.want)
		}
	}
}
