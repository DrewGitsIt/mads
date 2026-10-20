package changes

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// promptCap bounds ChangeTurn.Prompt (in runes).
const promptCap = 500

// Turns numbers the transcript's user turns: every Role "user" message
// with non-empty text and a timestamp, in transcript order, is turn
// N=1,2,…. Turn N's window runs from its At to the next turn's At. Messages
// without a timestamp can't be placed in time, so they are skipped; a
// transcript with none yields no turns, and the UI hides the strip.
// Files is left zero for Build to fill.
func Turns(msgs []contracts.TranscriptMessage) []contracts.ChangeTurn {
	turns := []contracts.ChangeTurn{}
	for _, m := range msgs {
		if m.Role != "user" || m.Timestamp.IsZero() {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		turns = append(turns, contracts.ChangeTurn{N: len(turns) + 1, Prompt: capRunes(text, promptCap), At: m.Timestamp})
	}
	return turns
}

func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n]))
}

// turnAt returns the turn number whose window holds t, or 0 when t is
// before the first turn (or there are no turns). turns must be ordered by
// At, which transcript order is; a transcript whose clock steps backwards
// is tolerated by searching on a sorted copy of the starts.
func turnAt(starts []time.Time, nums []int, t time.Time) int {
	i := sort.Search(len(starts), func(i int) bool { return starts[i].After(t) })
	if i == 0 {
		return 0
	}
	return nums[i-1]
}

// turnIndex prepares turnAt's inputs from turns.
func turnIndex(turns []contracts.ChangeTurn) (starts []time.Time, nums []int) {
	idx := make([]int, len(turns))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return turns[idx[a]].At.Before(turns[idx[b]].At) })
	for _, i := range idx {
		starts = append(starts, turns[i].At)
		nums = append(nums, turns[i].N)
	}
	return starts, nums
}

// FileTurns maps ledger timestamps to the ascending, de-duplicated turn
// numbers whose windows contain them. Never nil.
func FileTurns(turns []contracts.ChangeTurn, stamps []time.Time) []int {
	out := []int{}
	if len(turns) == 0 {
		return out
	}
	starts, nums := turnIndex(turns)
	seen := map[int]bool{}
	for _, t := range stamps {
		if n := turnAt(starts, nums, t); n > 0 && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}
