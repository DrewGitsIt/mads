package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// personas subcommand — the persona definition surface (agent-concept
// redesign §6) from the command line. The persona is a registry object
// (contracts.Agent, now narrower: prompt + allow/deny tool intents +
// quadrant + tags + capability refs, no harness-specific fields) and the
// data service owns the CRUD. These verbs are the thin CLI front to it:
//
//
//	mads personas list [-tag TAG] [-search TEXT] [-json] [-data URL]
//	mads personas get ID [-data URL]
//	mads personas new [-name NAME] [-description TEXT] [-prompt TEXT | -prompt-file PATH]
//	                   [-quadrant Q] [-tags a,b] [-allow read,write] [-deny shell] [-data URL]
//	mads personas delete ID [-yes] [-data URL]
//
// The -allow/-deny values are the closed ToolIntent vocabulary (read,
// write, edit, shell, search, fetch, skill, mcp) — the persona is
// harness-free by construction, so a concrete tool name won't be accepted
// (the data service also enforces this as a shape validation).

func cmdPersonas(args []string) int {
	if len(args) < 1 {
		printPersonasUsage()
		return 1
	}
	verb := args[0]
	rest := args[1:]

	// Every verb accepts a -data URL; find it once and strip it out of the
	// per-verb flag sets (matching the rest of the CLI's -data UX).
	dataBase := "http://localhost:17374"
	var scrubbed []string
	for _, a := range rest {
		switch {
		case a == "-data":
			// value is the next arg; it (and this flag) are dropped here
			// and re-added below where needed.
		case strings.HasPrefix(a, "-data="):
			dataBase = strings.TrimPrefix(a, "-data=")
		default:
			scrubbed = append(scrubbed, a)
		}
	}

	data := dataclient.New(dataBase, "user")
	ctx := context.Background()

	switch verb {
	case "list", "ls":
		return personasList(ctx, data, scrubbed)
	case "get":
		if len(scrubbed) != 1 {
			return personasErr("personas get: needs one persona id")
		}
		return personasGet(ctx, data, scrubbed[0])
	case "new", "create":
		return personasNew(ctx, data, scrubbed)
	case "delete", "rm":
		if len(scrubbed) < 1 {
			return personasErr("personas delete: needs one persona id")
		}
		return personasDelete(ctx, data, scrubbed)
	case "-h", "--help", "help":
		printPersonasUsage()
		return 0
	default:
		return personasErr("unknown personas verb: " + verb)
	}
}

func printPersonasUsage() {
	fmt.Fprintln(os.Stderr, `mads personas — manage persona definitions (agent-concept redesign §6)

Usage:
  mads personas list [-tag TAG] [-search TEXT] [-json]
  mads personas get ID
  mads personas new [-name NAME] [-description TEXT] [-prompt TEXT | -prompt-file PATH]
                    [-quadrant Q] [-tags a,b] [-allow read,write]
                    [-deny shell]
  mads personas delete ID [-yes]

All verbs accept -data URL (default http://localhost:17374).

The -allow/-deny values are the closed ToolIntent vocabulary (read, write,
edit, shell, search, fetch, skill, mcp). A persona is harness-free: no
concrete tool name, no model, no provider belongs in a persona.`)
}

func personasErr(msg string) int {
	fmt.Fprintln(os.Stderr, msg)
	return 1
}

func personasList(ctx context.Context, data *dataclient.Client, args []string) int {
	fs := flag.NewFlagSet("personas list", flag.ExitOnError)
	tag := fs.String("tag", "", "filter by a single tag (exact match, case-insensitive)")
	search := fs.String("search", "", "case-insensitive substring over name+description+tags")
	jsonOut := fs.Bool("json", false, "emit the full JSON records, not a table")
	_ = fs.Parse(args)

	personas, err := data.Agents(ctx)
	if err != nil {
		return personasErr("list personas: " + err.Error())
	}
	if *search != "" {
		n := strings.ToLower(*search)
		filtered := personas[:0]
		for _, a := range personas {
			if strings.Contains(strings.ToLower(a.Name), n) ||
				strings.Contains(strings.ToLower(a.Description), n) ||
				containsTag(a.Tags, n) {
				filtered = append(filtered, a)
			}
		}
		personas = filtered
	}
	if *tag != "" {
		w := strings.ToLower(*tag)
		filtered := personas[:0]
		for _, a := range personas {
			if containsTag(a.Tags, w) {
				filtered = append(filtered, a)
			}
		}
		personas = filtered
	}
	sort.Slice(personas, func(i, j int) bool { return personas[i].Name < personas[j].Name })

	if *jsonOut {
		raw, _ := json.MarshalIndent(personas, "", "  ")
		fmt.Println(string(raw))
		return 0
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tQUADRANT\tTAGS\tALLOW\tDENY\tSYS"); err != nil {
		return personasErr("personas list: " + err.Error())
	}
	for _, a := range personas {
		allow, deny := a.EffectiveToolIntents()
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			a.Name, a.Quadrant, strings.Join(a.Tags, ","),
			intentList(allow), intentList(deny), sysBool(a.System)); err != nil {
			return personasErr("personas list: " + err.Error())
		}
	}
	if err := w.Flush(); err != nil {
		return personasErr("personas list: " + err.Error())
	}
	return 0
}

func personasGet(ctx context.Context, data *dataclient.Client, id string) int {
	a, err := data.Agent(ctx, id)
	if err != nil {
		if err == dataclient.ErrNotFound {
			return personasErr("persona not found: " + id)
		}
		return personasErr(err.Error())
	}
	raw, _ := json.MarshalIndent(a, "", "  ")
	fmt.Println(string(raw))
	return 0
}

func personasNew(ctx context.Context, data *dataclient.Client, args []string) int {
	fs := flag.NewFlagSet("personas new", flag.ExitOnError)
	name := fs.String("name", "", "persona display name (required)")
	desc := fs.String("description", "", "one-line description (shown in Library cards + auto-pick)")
	prompt := fs.String("prompt", "", "the persona's system prompt (required unless -prompt-file)")
	promptFile := fs.String("prompt-file", "", "read the prompt from a file instead of -prompt")
	quadrant := fs.String("quadrant", "", "autonomous_executor|technical_partner|investigator|research_analyst")
	tags := fs.String("tags", "", "comma-separated tags")
	allow := fs.String("allow", "", "comma-separated ToolIntents allowed (read, write, edit, shell, search, fetch, skill, mcp)")
	deny := fs.String("deny", "", "comma-separated ToolIntents denied (overrides -allow)")
	_ = fs.Parse(args)

	if *name == "" {
		return personasErr("personas new: -name is required")
	}
	p := *prompt
	if *promptFile != "" {
		raw, err := os.ReadFile(*promptFile)
		if err != nil {
			return personasErr("read -prompt-file: " + err.Error())
		}
		p = string(raw)
	}
	if p == "" {
		return personasErr("personas new: -prompt (or -prompt-file) is required — the prompt IS the persona")
	}

	a := contracts.Agent{
		Name:        *name,
		Description: *desc,
		Prompt:      p,
		Quadrant:    contracts.Quadrant(*quadrant),
		Tags:        splitCSV(*tags),
		Allowlist:   splitIntents(*allow),
		Denylist:    splitIntents(*deny),
	}
	if err := data.CreateAgent(ctx, &a); err != nil {
		return personasErr("create persona: " + err.Error())
	}
	raw, _ := json.MarshalIndent(a, "", "  ")
	fmt.Println(string(raw))
	return 0
}

func personasDelete(ctx context.Context, data *dataclient.Client, args []string) int {
	fs := flag.NewFlagSet("personas delete", flag.ExitOnError)
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	_ = fs.Parse(args)
	id := ""
	for _, a := range fs.Args() {
		id = a
		break
	}
	if id == "" {
		return personasErr("personas delete: needs one persona id")
	}
	if !*yes {
		fmt.Printf("Delete persona %q? [y/N] ", id)
		var ans string
		if _, serr := fmt.Fscanln(os.Stdin, &ans); serr != nil {
			// EOF / Ctrl-C: treat as "no" (don't delete).
			fmt.Println("aborted")
			return 0
		}
		if !strings.EqualFold(ans, "y") && !strings.EqualFold(ans, "yes") {
			fmt.Println("aborted")
			return 0
		}
	}
	if err := data.DeleteAgent(ctx, id); err != nil {
		if err == dataclient.ErrNotFound {
			return personasErr("persona not found: " + id)
		}
		return personasErr(err.Error())
	}
	fmt.Println("deleted: " + id)
	return 0
}

// helpers -----------------------------------------------------------

func intentList(in []contracts.ToolIntent) string {
	if len(in) == 0 {
		return "-"
	}
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitIntents converts the -allow/-deny CSV into ToolIntents, dropping
// (and naming) any value outside the closed vocabulary — a concrete tool
// name in a persona is the harness leak the redesign removes, so the CLI
// refuses it loudly rather than silently accepting a name the data store
// would reject anyway.
func splitIntents(s string) []contracts.ToolIntent {
	var out []contracts.ToolIntent
	var bad []string
	for _, p := range splitCSV(s) {
		i := contracts.ToolIntent(p)
		if i.Valid() {
			out = append(out, i)
		} else {
			bad = append(bad, p)
		}
	}
	if len(bad) > 0 {
		fmt.Fprintf(os.Stderr, "personas: dropped unrecognized tool intent(s): %s (closed set: read, write, edit, shell, search, fetch, skill, mcp)\n",
			strings.Join(bad, ", "))
	}
	return out
}

func containsTag(tags []string, want string) bool {
	w := strings.ToLower(want)
	for _, t := range tags {
		if strings.ToLower(t) == w {
			return true
		}
	}
	return false
}

func sysBool(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
