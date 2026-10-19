package pipelinerun

import (
	"slices"
	"testing"
)

func TestSplitTaskIDs(t *testing.T) {
	for in, want := range map[string][]string{
		"":                nil,
		"tsk_a":           {"tsk_a"},
		"tsk_a,tsk_b":     {"tsk_a", "tsk_b"},
		" tsk_a , ,tsk_b": {"tsk_a", "tsk_b"},
	} {
		if got := SplitTaskIDs(in); !slices.Equal(got, want) {
			t.Errorf("SplitTaskIDs(%q) = %v, want %v", in, got, want)
		}
	}
}
