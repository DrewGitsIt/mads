package template

import (
	"strings"
	"testing"
)

func TestParseCondAndEval(t *testing.T) {
	cases := []struct {
		expr    string
		labels  []string
		onLoop  bool
		want    bool
		wantErr string
	}{
		{"", nil, false, true, ""},
		{`contains(labels, "trivial")`, []string{"trivial"}, false, true, ""},
		{`contains(labels, "trivial")`, []string{"big"}, false, false, ""},
		{`!contains(labels, "trivial")`, []string{"big"}, false, true, ""},
		{`!contains(labels, "trivial")`, []string{"trivial", "big"}, false, false, ""},
		{`! contains( task.labels , 'trivial' )`, nil, false, true, ""},
		{`equals(labels, "x")`, nil, false, false, "unknown when-expression"},
		{`contains(status, "done")`, nil, false, false, "only the task's labels"},
		{`contains(labels, trivial)`, nil, false, false, "quoted string"},
		{`contains(labels, "")`, nil, false, false, "must not be empty"},
		{`contains(labels "x")`, nil, false, false, "contains takes"},
		{"on_loop", nil, false, false, ""},
		{"on_loop", nil, true, true, ""},
		{"!on_loop", nil, false, true, ""},
		{"!on_loop", nil, true, false, ""},
	}
	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			cond, err := ParseCond(c.expr)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want error containing %q, got %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cond.Eval(c.labels, c.onLoop); got != c.want {
				t.Errorf("Eval(%v, %v) = %v, want %v", c.labels, c.onLoop, got, c.want)
			}
		})
	}
}

// A bad when-expression is a validation error — the editor and launch agree.
func TestValidateChecksWhen(t *testing.T) {
	src := []byte("name: t\nstages:\n  - name: a\n    agent: auto\n    when: 'nonsense()'\n")
	_, verr := Check(src, func(string) bool { return true })
	if verr == nil || !strings.Contains(verr.Error(), "when") {
		t.Fatalf("bad when accepted: %v", verr)
	}
}
