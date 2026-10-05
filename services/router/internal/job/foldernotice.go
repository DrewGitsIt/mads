package job

import (
	"context"
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

const FolderNoticeKind = "folder_notice"

// folderNoticeMaxFiles caps how many changed files the notice names; the
// rest are counted.
const folderNoticeMaxFiles = 20

// NotifyFolderChanged is the job body for a pipeline task merged into an
// isolated session's folder while its agent was still working
// (docs/SESSION-WORKTREES-DESIGN.md D8, merge.completed with waited_out):
// it tells the agent, in one system-attributed message, which files changed
// under it, so it re-reads them before editing over the merge. The
// pipeline process can't reach the session child; the Router can. Only a
// live session is told — a dormant one starts its next turn from the files
// as they are, and the conversation's card already says what changed.
// Delivered through the child's injection queue, so a mid-turn agent reads
// it when its turn ends. Best-effort: a failed delivery fails only the job.
func NotifyFolderChanged(sessionID, taskTitle string, files []string) Func {
	return func(ctx context.Context, deps Deps) error {
		if deps.Resolver == nil || len(files) == 0 {
			return nil
		}
		addr, ok := deps.Resolver.SessionAddr(sessionID)
		if !ok {
			return nil
		}
		if err := deliverDigest(ctx, addr, FolderNotice(taskTitle, files), contracts.Attribution{SourceName: implicitSourceName}); err != nil {
			return fmt.Errorf("folder notice: deliver to %s: %w", sessionID, err)
		}
		return nil
	}
}

// FolderNotice is the message NotifyFolderChanged sends.
func FolderNotice(taskTitle string, files []string) string {
	task := "A pipeline task"
	if t := strings.TrimSpace(taskTitle); t != "" {
		task = fmt.Sprintf("The pipeline task %q", t)
	}
	shown := files
	if len(shown) > folderNoticeMaxFiles {
		shown = shown[:folderNoticeMaxFiles]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s merged into your folder while you were working; these files changed:\n", task)
	for _, f := range shown {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	if more := len(files) - len(shown); more > 0 {
		fmt.Fprintf(&b, "- … and %d more\n", more)
	}
	b.WriteString("Re-read them before editing.")
	return b.String()
}
