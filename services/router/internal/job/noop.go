package job

import (
	"context"
	"fmt"

	"github.com/drewkidwell/mads/providers/fastmodel"
)

const NoopKind = "noop"

// Noop is the sample job proving the spawn -> work -> exit contract: it
// touches the Data Service and the Fast Model through nothing but its
// Deps, then returns.
func Noop(ctx context.Context, deps Deps) error {
	if _, err := deps.Data.Events(ctx, 0, "", nil); err != nil {
		return fmt.Errorf("noop job: data service: %w", err)
	}
	if _, _, err := deps.FastModel.DoText(ctx, fastmodel.Job{Prompt: "ping"}); err != nil {
		return fmt.Errorf("noop job: fast model: %w", err)
	}
	return nil
}
