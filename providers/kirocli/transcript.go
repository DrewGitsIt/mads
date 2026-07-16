package kirocli

import (
	"context"
	"fmt"

	"github.com/drewkidwell/mads/providers/provider"
)

// Transcript reads session history via ACP replay rather than an on-disk
// file: kiro-cli has no documented transcript format the way claude-code's
// project JSONL is documented, but its ACP agent advertises
// loadSession: true, and the standard convention that capability implies is
// that session/load replays prior turns as session/update notifications
// before its own response resolves. startACP buffers exactly that replay
// (see session.go's replaying flag) — this is the least-certain row in the
// Matrix; see README.md.
func (p *Provider) Transcript(ctx context.Context, ref string) ([]provider.Message, error) {
	if ref == "" {
		return nil, fmt.Errorf("transcript: empty session ref")
	}
	sess, err := p.startACP(ctx, provider.StartOptions{Mode: provider.ModeACP, Resume: ref})
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	return sess.(*streamSession).drainReplay(), nil
}
