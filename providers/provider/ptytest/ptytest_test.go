package ptytest

import (
	"io"
	"reflect"
	"testing"
)

// chunks yields its parts one Read at a time, so a test can split a paste
// marker across reads.
type chunks []string

func (c *chunks) Read(p []byte) (int, error) {
	if len(*c) == 0 {
		return 0, io.EOF
	}
	n := copy(p, (*c)[0])
	(*c)[0] = (*c)[0][n:]
	if (*c)[0] == "" {
		*c = (*c)[1:]
	}
	return n, nil
}

func TestPromptSubmissions(t *testing.T) {
	cases := []struct {
		name string
		in   chunks
		want []string
	}{
		{"typed lines submit at each newline", chunks{"one\ntwo\r"}, []string{"one", "two"}},
		{"a paste is one message", chunks{"\x1b[200~one\ntwo\r\nthree\x1b[201~\r"}, []string{"one\ntwo\r\nthree"}},
		{"markers split across reads", chunks{"\x1b[2", "00~a\nb\x1b", "[201", "~\r"}, []string{"a\nb"}},
		{"typed text around a paste joins it", chunks{"say \x1b[200~a\nb\x1b[201~ ok\r"}, []string{"say a\nb ok"}},
		{"nothing submitted without a return", chunks{"\x1b[200~a\nb\x1b[201~"}, nil},
		{"a stray escape is text", chunks{"a\x1b[2xb\r"}, []string{"a\x1b[2xb"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			in := tc.in
			if err := (Prompt{Submit: func(s string) { got = append(got, s) }}).Read(&in); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("submitted %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPromptCtrlCOutsideAPasteOnly(t *testing.T) {
	ctrlC := 0
	in := chunks{"\x03\x1b[200~\x03\x1b[201~\r"}
	var got []string
	err := Prompt{Submit: func(s string) { got = append(got, s) }, CtrlC: func() { ctrlC++ }}.Read(&in)
	if err != nil {
		t.Fatal(err)
	}
	if ctrlC != 1 || !reflect.DeepEqual(got, []string{"\x03"}) {
		t.Fatalf("ctrl-c %d, submitted %q; want 1 and the pasted byte", ctrlC, got)
	}
}
