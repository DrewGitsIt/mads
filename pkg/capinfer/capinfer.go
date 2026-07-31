// Package capinfer is the one inference mechanism behind every "mads,
// figure out which capabilities this needs" moment: the task editor's
// suggested-capability chips and interactive session-start's agent auto
// staffing both call the same Inferrer (owner decision, 2026-07-30 —
// suggest, never require, and never diverge into two heuristics for the
// same job).
//
// Free text (a task's title+description, or a session's opening prompt) in,
// registry capability names out — strictly a subset of the live registry,
// same grounding rule as pkg/agentgen: a name the model invents that isn't
// in the catalog is silently dropped, never suggested. A cheap,
// non-LLM deterministic signal (git remote host → capability name)
// augments the model's suggestions and runs even when no Fast Model is
// configured.
package capinfer

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

const system = `You suggest which capabilities a piece of work needs, given free text describing it (a task's ` +
	`title and description, or a session's opening prompt) and a list of the capabilities actually available. ` +
	`Only ever name capabilities from the "Available capabilities" list below — never invent one that isn't ` +
	`listed; reply with an empty list if nothing available fits. Reply with ONLY a JSON object of this exact ` +
	`shape, no prose, no code fences:

{"capabilities": ["name1", "name2"]}`

// Inferrer runs capability inference against a live Data Service and Fast
// Model — mirrors agentgen.Generator's shape.
type Inferrer struct {
	Data      *dataclient.Client
	FastModel *fastmodel.Runner
}

// New builds an Inferrer.
func New(data *dataclient.Client, fm *fastmodel.Runner) *Inferrer {
	return &Inferrer{Data: data, FastModel: fm}
}

// suggestion is the Fast Model's structured reply shape.
type suggestion struct {
	Capabilities []string `json:"capabilities"`
}

// Infer suggests registry capability names for text, unioned with
// RepoSignals over repos. Never a hard failure: a nil/unconfigured
// FastModel, an empty registry, or a Fast Model that never produces usable
// output all degrade to "no model-derived suggestions" rather than an
// error — repo signals (if any) still come back. The only errors returned
// are live infrastructure failures (e.g. the Data Service is unreachable).
func (inf *Inferrer) Infer(ctx context.Context, text string, repos []contracts.WorkspaceRepo) ([]string, error) {
	registry, err := inf.Data.Capabilities(ctx)
	if err != nil {
		return nil, fmt.Errorf("capinfer: list capabilities: %w", err)
	}

	seen := make(map[string]struct{})
	var out []string
	add := func(names []string) {
		for _, n := range names {
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}

	add(RepoSignals(repos, registry))

	modelNames, err := inf.inferFromModel(ctx, text, registry)
	if err != nil {
		return nil, err
	}
	add(modelNames)

	sort.Strings(out)
	return out, nil
}

// inferFromModel runs the Fast Model call and grounds its reply against
// registry — every suggested name outside the catalog is dropped, never
// surfaced. A skippable Fast Model failure (no backend configured, or a
// configured one never produced usable output) is not an error: it's
// "nothing to suggest from the model," same as agentgen's generation path
// treats an absent backend.
func (inf *Inferrer) inferFromModel(ctx context.Context, text string, registry []contracts.Capability) ([]string, error) {
	if inf.FastModel == nil || strings.TrimSpace(text) == "" || len(registry) == 0 {
		return nil, nil
	}

	var out suggestion
	prompt := text + registrySection(registry)
	if _, err := inf.FastModel.Do(ctx, fastmodel.Job{System: system, Prompt: prompt}, &out); err != nil {
		if fastmodel.IsSkippable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("capinfer: infer: %w", err)
	}

	known := knownNames(registry)
	kept := make([]string, 0, len(out.Capabilities))
	for _, name := range out.Capabilities {
		if _, ok := known[name]; ok {
			kept = append(kept, name)
		}
	}
	return kept, nil
}

func knownNames(registry []contracts.Capability) map[string]struct{} {
	known := make(map[string]struct{}, len(registry))
	for _, c := range registry {
		known[c.Name] = struct{}{}
	}
	return known
}

func registrySection(registry []contracts.Capability) string {
	var b strings.Builder
	b.WriteString("\n\nAvailable capabilities:")
	for _, c := range registry {
		fmt.Fprintf(&b, "\n- %s", c.Name)
		if c.Description != "" {
			fmt.Fprintf(&b, ": %s", c.Description)
		}
	}
	return b.String()
}

// remoteHostCapabilities maps a git remote's host to the registry
// capability name(s) it implies, when that capability actually exists
// (owner decision: deterministic signals are grounded exactly like model
// output — never suggest a capability the registry doesn't have).
var remoteHostCapabilities = map[string]string{
	"github.com": "github",
	"gitlab.com": "gitlab",
}

// RepoSignals returns registry capability names implied by cheap,
// deterministic facts about repos — currently, each repo's git remote host
// (github.com -> "github", gitlab.com -> "gitlab") when a same-named
// capability exists in registry. Runs independently of the Fast Model (no
// LLM call, no network beyond a local `git remote` read), so it's always
// available even with no Fast Model configured. Best-effort: a repo with no
// remote, an unreadable path, or a host with no matching registry
// capability contributes nothing, never an error.
func RepoSignals(repos []contracts.WorkspaceRepo, registry []contracts.Capability) []string {
	if len(repos) == 0 || len(registry) == 0 {
		return nil
	}
	known := knownNames(registry)
	seen := make(map[string]struct{})
	var out []string
	for _, repo := range repos {
		host, ok := remoteHost(repo.Path)
		if !ok {
			continue
		}
		name, ok := remoteHostCapabilities[host]
		if !ok {
			continue
		}
		if _, ok := known[name]; !ok {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// remoteHost best-effort resolves the host of a repo's origin remote, e.g.
// "github.com" for both "git@github.com:org/repo.git" and
// "https://github.com/org/repo.git". Never errors outward — a missing repo,
// a repo with no origin remote, or a git binary that isn't on PATH all just
// yield ok=false.
func remoteHost(path string) (host string, ok bool) {
	if path == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	//nolint:gosec // path is workspace/operator-configured, not raw user input
	cmd := exec.CommandContext(ctx, "git", "-C", path, "remote", "get-url", "origin")
	raw, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return parseRemoteHost(strings.TrimSpace(string(raw)))
}

// parseRemoteHost extracts the host from either remote URL form git uses:
// scp-like ("git@host:org/repo.git") or a real URL ("https://host/org/repo").
func parseRemoteHost(remote string) (string, bool) {
	if remote == "" {
		return "", false
	}
	if i := strings.Index(remote, "://"); i >= 0 {
		remote = remote[i+3:]
	} else if i := strings.Index(remote, "@"); i >= 0 {
		remote = remote[i+1:]
		remote = strings.Replace(remote, ":", "/", 1)
	}
	host := remote
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return "", false
	}
	return host, true
}
