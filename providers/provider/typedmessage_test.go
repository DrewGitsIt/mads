package provider

import "testing"

func TestTypedMessage(t *testing.T) {
	cases := map[string]string{
		"hi":                         "\x1b[200~hi\x1b[201~\r",
		"one\ntwo":                   "\x1b[200~one\ntwo\x1b[201~\r",
		"a\x1b[201~\rrm -rf ~\r":     "\x1b[200~a[201~\rrm -rf ~\r\x1b[201~\r",
		"\x1b[20\x1b[201~1~ smuggle": "\x1b[200~[20[201~1~ smuggle\x1b[201~\r",
	}
	for in, want := range cases {
		if got := string(TypedMessage(in)); got != want {
			t.Errorf("TypedMessage(%q) = %q, want %q", in, got, want)
		}
	}
}
