package api

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/router/internal/gitstate"
)

// Git state served from the Router (docs/GIT-STATE-DESIGN.md): GET
// /changeset and GET /repos answer from the gitstate service's kept
// state; these are the computes it runs in the background, the same ones
// those routes used to run per request.

// GitStateEmitter appends session.git_state_updated through data.
func GitStateEmitter(data *dataclient.Client) func(ctx context.Context, p contracts.GitStateUpdatedPayload) error {
	return func(ctx context.Context, p contracts.GitStateUpdatedPayload) error {
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return data.Append(ctx, &contracts.Envelope{Type: contracts.SessionGitStateUpdated, SessionID: p.SessionID, Payload: raw})
	}
}

func (s *server) emitGitState(ctx context.Context, p contracts.GitStateUpdatedPayload) error {
	if s.data == nil {
		return nil
	}
	return GitStateEmitter(s.data)(ctx, p)
}

func (s *server) gitStateComputers() gitstate.Computers {
	return gitstate.Computers{
		Repos: s.computeSessionRepos,
		ChangeSet: func(ctx context.Context, sessionID, vs string) (contracts.ChangeSet, error) {
			sess, err := s.data.Session(ctx, sessionID)
			if err != nil {
				return contracts.ChangeSet{}, err
			}
			set, _, err := s.computeChangeSet(ctx, sess, vs)
			return set, err
		},
		Counts: s.recountForGitState,
	}
}

// computeSessionRepos is every repo of the session's set — the ones with
// nothing uncommitted too, since they may still have commits to push —
// with each checkout's branch facts, and for an isolated session the base
// in the user's checkout, which a push after Land publishes. A repo whose
// facts can't be read (deleted since) is left out. Checkouts is every
// checkout the set works in, for the repo watcher.
func (s *server) computeSessionRepos(ctx context.Context, sessionID string) (gitstate.Repos, error) {
	primary, all, _, err := s.worktreeOps().Repos(ctx, sessionID)
	if err != nil {
		return gitstate.Repos{}, err
	}
	out := gitstate.Repos{State: []contracts.SessionRepoState{}}
	for i, t := range all {
		out.Checkouts = append(out.Checkouts, t.Folder)
		if t.Isolated() {
			out.Checkouts = append(out.Checkouts, t.Primary)
		}
		wt, err := s.repoWorktree(ctx, t.Folder)
		if err != nil {
			continue
		}
		rs := contracts.SessionRepoState{Root: t.Folder, Primary: primary != nil && i == 0, Isolated: t.Isolated(), Base: t.Base, Worktree: wt}
		if t.Isolated() {
			if base, err := s.repoBranch(ctx, t.Primary, t.Base); err == nil {
				rs.BaseWorktree = &base
			}
		}
		out.State = append(out.State, rs)
	}
	return out, nil
}

// recountForGitState is the counts part: the header recount, routed as
// POST /worktree/refresh always was (the child's coalesced recount when
// one is live, the Router's own otherwise), then the counts it left on
// the record, as the part's fingerprint.
//
// A recount that finds the session working in a repo it didn't before (or
// no longer) asks for its repos too: file.changed alone recomputes only
// the change set and counts, and the counts are where a new repo shows
// first.
func (s *server) recountForGitState(ctx context.Context, sessionID string) (string, error) {
	before, beforeErr := s.data.Session(ctx, sessionID)
	if err := s.recount(ctx, sessionID); err != nil {
		return "", err
	}
	rec, err := s.data.Session(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if beforeErr == nil && !slices.Equal(repoRoots(before.Repos), repoRoots(rec.Repos)) {
		s.gitState.Trigger(sessionID, contracts.GitPartRepos)
	}
	b, _ := json.Marshal(struct {
		U, L, O int
		R       []contracts.SessionRepo
	}{rec.UncommittedCount, rec.UnlandedCount, rec.OutsideCount, rec.Repos})
	return string(b), nil
}

func repoRoots(repos []contracts.SessionRepo) []string {
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		out = append(out, r.Root)
	}
	slices.Sort(out)
	return out
}

// changeSetResponse is GET /changeset's body: the set plus whether a
// recompute is queued or running. ComputedAt shadows the set's own, so a
// cold answer (never computed) says "" rather than the zero time.
type changeSetResponse struct {
	contracts.ChangeSet
	ComputedAt string `json:"computed_at"`
	Updating   bool   `json:"updating"`
}

// coldChangeSet is the answer before a first compute: nothing, updating.
func coldChangeSet(vs string) changeSetResponse {
	return changeSetResponse{
		ChangeSet: contracts.ChangeSet{
			Baseline:  vs,
			Baselines: []contracts.ChangeBaseline{},
			Files:     []contracts.ChangedFile{},
			Turns:     []contracts.ChangeTurn{},
			Commits:   []contracts.ChangeCommit{},
		},
		Updating: true,
	}
}
