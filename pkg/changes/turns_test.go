package changes

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestTurns(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	msgs := []contracts.TranscriptMessage{
		{Role: "user", Text: "  first ask  ", Timestamp: t0},
		{Role: "agent", Text: "ok", Timestamp: t0.Add(time.Second)},
		{Role: "tool_result", Text: "out", Timestamp: t0.Add(2 * time.Second)},
		{Role: "user", Text: "no timestamp"},
		{Role: "user", Text: "   ", Timestamp: t0.Add(3 * time.Second)},
		{Role: "notification", Text: "task done", Timestamp: t0.Add(4 * time.Second)},
		{Role: "user", Text: strings.Repeat("é", 600), Timestamp: t0.Add(time.Minute)},
	}
	got := Turns(msgs)
	if len(got) != 2 {
		t.Fatalf("Turns = %+v, want 2 turns", got)
	}
	if got[0].N != 1 || got[0].Prompt != "first ask" || !got[0].At.Equal(t0) {
		t.Errorf("turn 1 = %+v", got[0])
	}
	if got[1].N != 2 || len([]rune(got[1].Prompt)) != promptCap {
		t.Errorf("turn 2 = N %d, %d runes; want N 2, %d runes", got[1].N, len([]rune(got[1].Prompt)), promptCap)
	}
}

func TestTurnsNoTimestamps(t *testing.T) {
	got := Turns([]contracts.TranscriptMessage{{Role: "user", Text: "hi"}, {Role: "user", Text: "again"}})
	if got == nil || len(got) != 0 {
		t.Fatalf("Turns = %#v, want empty non-nil", got)
	}
	if ft := FileTurns(got, []time.Time{time.Now()}); ft == nil || len(ft) != 0 {
		t.Errorf("FileTurns = %#v, want empty non-nil", ft)
	}
}

func TestFileTurnsWindows(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	turns := []contracts.ChangeTurn{
		{N: 1, At: t0},
		{N: 2, At: t0.Add(10 * time.Minute)},
		{N: 3, At: t0.Add(20 * time.Minute)},
	}
	for _, tc := range []struct {
		name   string
		stamps []time.Duration
		want   []int
	}{
		{"before first turn", []time.Duration{-time.Minute}, []int{}},
		{"window start inclusive", []time.Duration{0}, []int{1}},
		{"window end exclusive", []time.Duration{10 * time.Minute}, []int{2}},
		{"last turn open-ended", []time.Duration{5 * time.Hour}, []int{3}},
		{"several, deduped and sorted", []time.Duration{25 * time.Minute, time.Minute, 2 * time.Minute, 21 * time.Minute}, []int{1, 3}},
	} {
		var stamps []time.Time
		for _, d := range tc.stamps {
			stamps = append(stamps, t0.Add(d))
		}
		if got := FileTurns(turns, stamps); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: FileTurns = %v, want %v", tc.name, got, tc.want)
		}
	}
}
