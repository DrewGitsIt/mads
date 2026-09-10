package template

import (
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"gopkg.in/yaml.v3"
)

// Duration must parse the same human strings time.ParseDuration accepts,
// and MarshalYAML must spell them back out the same way — not as the
// int64 nanosecond count time.Duration itself would produce, and not with
// time.Duration.String()'s noisy trailing zero units ("15m0s").
func TestDurationRoundTrip(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"15m", 15 * time.Minute},
		{"2h30m", 2*time.Hour + 30*time.Minute},
		{"500ms", 500 * time.Millisecond},
		{"1h", time.Hour},
		{"30s", 30 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			var d Duration
			if err := yaml.Unmarshal([]byte(c.in), &d); err != nil {
				t.Fatalf("unmarshal %q: %v", c.in, err)
			}
			if time.Duration(d) != c.want {
				t.Fatalf("unmarshal %q = %v, want %v", c.in, time.Duration(d), c.want)
			}
			out, err := yaml.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.TrimSpace(string(out))
			if got != c.in {
				t.Errorf("marshal %v = %q, want %q (must round-trip to what the user typed)", time.Duration(d), got, c.in)
			}
		})
	}
}

// A bad duration string must not abort the whole document decode: it's
// remembered as the invalidDuration sentinel and left for Validate to
// report as a normal field error (see invalidDuration's doc comment).
func TestDurationUnmarshalBadStringDefersFailure(t *testing.T) {
	var d Duration
	if err := yaml.Unmarshal([]byte("banana"), &d); err != nil {
		t.Fatalf("UnmarshalYAML must not fail hard on a bad string, got %v", err)
	}
	if d.valid() {
		t.Fatal("bad duration string should leave the sentinel, not a valid-looking zero value")
	}
}

// checkDuration must distinguish "the key was never written" (fine, nil)
// from "the key was written as zero" (an error, non-nil pointing at 0) even
// though both would leave the same Duration(0) if Timeout weren't a
// pointer — see checkDuration's doc comment.
func TestCheckDuration(t *testing.T) {
	zero := Duration(0)
	positive := Duration(time.Minute)
	negative := Duration(-time.Minute)
	invalid := invalidDuration
	cases := []struct {
		name    string
		d       *Duration
		wantErr string
	}{
		{"omitted", nil, ""},
		{"explicit zero", &zero, "greater than zero"},
		{"explicit positive", &positive, ""},
		{"invalid string", &invalid, "not a valid duration"},
		{"explicit negative", &negative, "greater than zero"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var v contracts.ValidationError
			checkDuration(&v, "x.timeout", c.d)
			if c.wantErr == "" {
				if !v.Empty() {
					t.Fatalf("unexpected error(s): %v", v.Error())
				}
				return
			}
			if v.Empty() || !strings.Contains(v.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, v.Error())
			}
		})
	}
}
