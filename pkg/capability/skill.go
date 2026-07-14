package capability

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// probeTimeout bounds the remote-MCP reachability probe in Health.
const probeTimeout = 3 * time.Second

// expandPath expands a leading "~/" to the user's home directory; every
// other path is returned unchanged.
func expandPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// Health reports whether cap is currently working (spec §12.6): for a
// skill, its directory exists; for MCP, running reports process liveness
// (local) or is ignored in favor of a short reachability probe against
// Config.URL (remote). Never touches credentials — a missing credential is
// reported by the caller (Runner), not detected here.
func Health(ctx context.Context, cap contracts.Capability, running bool) (healthy bool, reason string) {
	switch cap.Type {
	case contracts.CapabilitySkill:
		if cap.Config.Path == "" {
			return false, "no skill path configured"
		}
		path := expandPath(cap.Config.Path)
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return false, "skill directory not found: " + cap.Config.Path
		}
		return true, ""
	case contracts.CapabilityMCP:
		if cap.Config.URL != "" {
			if !probeURL(ctx, cap.Config.URL) {
				return false, "server unreachable: " + cap.Config.URL
			}
			return true, ""
		}
		if !running {
			return false, "server not running"
		}
		return true, ""
	default:
		return false, "unknown capability type"
	}
}

func probeURL(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// InstallSkill sets up a skill's availability in cwd for providers that
// read a skill directory convention (today: claude-code's
// .claude/skills/<name>, spec §12: "industry-standard skill format").
// Best-effort: log-and-continue on failure, mirroring Runner.installHook's
// existing pattern — never fails session start.
func InstallSkill(cap contracts.Capability, cwd string) error {
	if cap.Type != contracts.CapabilitySkill || cap.Config.Path == "" {
		return nil
	}
	dir := filepath.Join(cwd, ".claude", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("capability %s: create skills dir: %w", cap.Name, err)
	}
	link := filepath.Join(dir, cap.Name)
	_ = os.Remove(link)
	if err := os.Symlink(expandPath(cap.Config.Path), link); err != nil {
		return fmt.Errorf("capability %s: symlink skill: %w", cap.Name, err)
	}
	return nil
}

// SkillContent reads the injectable text for a skill capability — SKILL.md
// under Config.Path if present, else the path itself if it's a plain file.
// This is what the three injection paths (spec §12.4) send as a turn.
func SkillContent(cap contracts.Capability) (string, error) {
	path := expandPath(cap.Config.Path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("capability %s: %w", cap.Name, err)
	}
	if info.IsDir() {
		path = filepath.Join(path, "SKILL.md")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("capability %s: read skill content: %w", cap.Name, err)
	}
	return string(data), nil
}
