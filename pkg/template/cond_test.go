package template

import (
	"strings"
	"testing"
)

func TestParseCondAndEval(t *testing.T) {
	cases := []struct {
		expr    string
		labels  []string
		want    bool
		wantErr string
	}{
		{"", nil, true, ""},
		{`contains(labels, "trivial")`, []string{"trivial"}, true, ""},
		{`contains(labels, "trivial")`, []string{"big"}, false, ""},
		{`!contains(labels, "trivial")`, []string{"big"}, true, ""},
		{`!contains(labels, "trivial")`, []string{"trivial", "big"}, false, ""},
		{`! contains( task.labels , 'trivial' )`, nil, true, ""},
		{`equals(labels, "x")`, nil, false, "unknown when-expression"},
		{`contains(status, "done")`, nil, false, "only the task's labels"},
		{`contains(labels, trivial)`, nil, false, "quoted string"},
		{`contains(labels, "")`, nil, false, "must not be empty"},
		{`contains(labels "x")`, nil, false, "contains takes"},
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
			if got := cond.Eval(c.labels); got != c.want {
				t.Errorf("Eval(%v) = %v, want %v", c.labels, got, c.want)
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
