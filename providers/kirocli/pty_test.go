package kirocli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/providers/provider/ptytest"
)

// TestPTYSendMultiLineIsOneMessage: a multi-line Send reaches the stand-in
// prompt (fkTerminal reads its input as ptytest.Prompt does) as one
// message — one reply covering every line — where the same text typed bare
// submits at each newline.
func TestPTYSendMultiLineIsOneMessage(t *testing.T) {
	text := "echo one\ntwo\nthree"
	for _, tc := range []struct {
		name string
		send func(provider.PTYSession) error
		want []string
	}{
		{"Send", func(s provider.PTYSession) error { return s.Send(context.Background(), text) },
			[]string{"you said one\ntwo\nthree"}},
		{"typed bare", func(s provider.PTYSession) error { _, err := s.Terminal().Write([]byte(text + "\r")); return err },
			[]string{"you said one", "ok", "ok"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess, err := testProvider(t).Start(context.Background(), provider.StartOptions{Mode: provider.ModePTY, Cwd: t.TempDir()})
			if err != nil {
				t.Fatalf("start pty: %v", err)
			}
			defer func() { _ = sess.Close() }()
			pty := sess.(provider.PTYSession)
			screen := ptytest.Watch(pty.Terminal())
			if !screen.WaitFor("> ", 10*time.Second) {
				t.Fatal("no prompt")
			}
			if err := tc.send(pty); err != nil {
				t.Fatalf("send: %v", err)
			}
			want := "> " + strings.Join(tc.want, "\n> ") + "\n> "
			if !screen.WaitFor(want, 10*time.Second) {
				t.Fatalf("screen %q, want %q", screen.Text(), want)
			}
		})
	}
}
