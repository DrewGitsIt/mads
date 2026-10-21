package job

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/linkwrap"
)

// subjectTurnTimeout bounds subjectTurn's transcript read: the turn number
// is a nicety on the digest's label, never worth holding a delivery for.
const subjectTurnTimeout = 5 * time.Second

// subjectTurn is the turn a digest of sessionID covers: its transcript's
// current turn count, in get_session_turns' turn model (linkwrap.SplitTurns),
// read from the subject's running child — a digest fires on that child's
// own turn end, so it is normally up. 0 (the turn attribute omitted) when
// there is no one to deliver to, the child isn't reachable, or the read
// fails.
func subjectTurn(ctx context.Context, deps Deps, sessionID string, needed bool) int {
	if !needed || deps.Resolver == nil {
		return 0
	}
	addr, ok := deps.Resolver.SessionAddr(sessionID)
	if !ok {
		return 0
	}
	msgs, err := fetchTranscript(ctx, addr)
	if err != nil {
		log.Printf("digest: read %s transcript for turn number: %v", sessionID, err)
		return 0
	}
	return len(linkwrap.SplitTurns(msgs))
}

func fetchTranscript(ctx context.Context, addr string) ([]contracts.TranscriptMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, subjectTurnTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/transcript", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var msgs []contracts.TranscriptMessage
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}
