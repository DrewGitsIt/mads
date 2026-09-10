package template

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that speaks Go's human duration syntax
// ("15m", "2h30m", "500ms") on the wire instead of the raw int64 nanosecond
// count time.Duration marshals to by default. It backs every timeout: and
// backoff: field in the template grammar (spec §6).
type Duration time.Duration

// invalidDuration is the sentinel a Duration is set to when its YAML source
// failed time.ParseDuration.
//
// UnmarshalYAML deliberately does not return an error in that case. Parse()
// unmarshals an entire template document in one yaml.Unmarshal call, and an
// error escaping from a nested field's UnmarshalYAML aborts that call —
// which would report only one opaque failure on the "source" field and bury
// every OTHER problem in the same document, violating the report-everything-
// together rule (spec §10.3) that the rest of this package follows. So a
// bad duration string is remembered as this sentinel instead, and Validate —
// which already walks every stage collecting errors — turns it into an
// ordinary field-scoped error alongside whatever else is wrong.
//
// math.MinInt64 is used because no duration a template would plausibly
// write can land on it: it is the extreme negative edge of what
// time.ParseDuration can even represent (about -2,562,047 hours), reachable
// only by an input engineered to hit that exact nanosecond count. Any
// timeout: or backoff: a person actually types is nowhere near it.
const invalidDuration Duration = Duration(math.MinInt64)

// valid reports whether d is a real parsed value rather than the
// invalidDuration sentinel left behind by a bad YAML string.
func (d Duration) valid() bool {
	return d != invalidDuration
}

// UnmarshalYAML parses the YAML scalar as a Go duration string. See
// invalidDuration for why a parse failure is swallowed here rather than
// returned.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		// Not a scalar at all (e.g. a list or mapping where a string was
		// expected) — that's a genuine structural error, not merely a bad
		// duration spelling, so let yaml.v3 report it the normal way.
		return err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		*d = invalidDuration
		return nil
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML renders the duration in the same string form it parses
// ("15m"), never the underlying int64 nanoseconds. This matters because the
// template editor round-trips every template through Parse then Marshal on
// each visual edit; marshaling as an integer would silently rewrite a
// user's "15m" to "900000000000" the moment they touched any unrelated
// field.
func (d Duration) MarshalYAML() (interface{}, error) {
	return formatDuration(time.Duration(d)), nil
}

// formatDuration renders d the way a human would have typed it, which
// time.Duration.String() does not: String() always spells out every unit
// down through seconds once any higher unit is present, so 15 minutes
// becomes "15m0s" and 2h30m becomes "2h30m0s". Feeding that back through
// the editor's round-trip would visibly rewrite what the user wrote, so
// this instead emits only the units that are actually nonzero.
func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	neg := d < 0
	if neg {
		d = -d
	}
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	// d is now the sub-minute remainder; time.Duration.String() never pads
	// that part with spurious units, so it's already the compact form we
	// want ("30s", "500ms", "1.5s", ...).
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	if h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	if d > 0 || (h == 0 && m == 0) {
		b.WriteString(d.String())
	}
	return b.String()
}

// checkDuration validates a timeout-shaped *Duration field: nil (the YAML
// key omitted entirely) is fine — that's "no override, fall back to
// defaults: then the engine's own default" — but a present value must have
// actually parsed and must be strictly positive. A zero or negative timeout
// is never useful: it either fires before the stage can do anything or (per
// the meaning some tools give zero) never fires at all, and either reading
// is a surprise worth rejecting up front.
//
// d is a pointer rather than a plain Duration specifically so nil can mean
// "omitted": a present "timeout: 0" and an absent timeout: would otherwise
// be indistinguishable, since time.ParseDuration("0") succeeds and a plain
// Duration has no other way to carry "the key wasn't there at all". A
// pointer field also means yaml.v3 gives us that distinction for free —
// nil unless the key appeared in the source — with no extra bookkeeping.
func checkDuration(v *contracts.ValidationError, field string, d *Duration) {
	if d == nil {
		return
	}
	if !d.valid() {
		v.Add(field, fmt.Sprintf("not a valid duration — use a Go duration string like %q", "15m"))
		return
	}
	if time.Duration(*d) <= 0 {
		v.Add(field, "must be greater than zero")
	}
}
