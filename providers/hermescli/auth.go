package hermescli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/drewkidwell/mads/pkg/binlocate"
	"github.com/drewkidwell/mads/pkg/hermesinstall"
	"github.com/drewkidwell/mads/providers/provider"
)

// Signing back in (docs/AUTH-SIGNIN-DESIGN.md). hermes never fails a prompt
// over the ACP wire: a turn its provider refused still resolves stopReason
// "end_turn", with hermes's own sentence as the agent's text
// (acp_adapter/server.py prompt/_finish_turn). What it does record is the
// failed-turn boundary row it writes to state.db before answering —
// display_kind "failed_turn" with display_metadata {"error",
// "error_surface"} (agent/conversation_loop.py _close_durable_failed_turn,
// _failed_turn_display_metadata). Source lines are against hermes-agent
// checkout d923a5eb48.

var (
	_ provider.AccountKeyer  = (*Provider)(nil)
	_ provider.Authenticator = (*Provider)(nil)
)

// accountPrefix starts every hermes account key (accountKey).
const accountPrefix = "hermes:"

// accountKey names the login a hermes process with environment env runs on:
// the provider plus its home ($HERMES_HOME, else ~/.hermes), which holds
// auth.json and every credential pool in it. Two sessions with the same key
// share one login. "" when the home can't be made absolute.
func accountKey(env []string) string {
	home := homeFrom(env)
	if !filepath.IsAbs(home) {
		return ""
	}
	return accountPrefix + filepath.Clean(home)
}

// DefaultAccount implements provider.AccountKeyer: the login a session
// started with no extra environment runs on.
func (p *Provider) DefaultAccount() string { return accountKey(p.childEnv()) }

// storedFailure is what a failed-turn row's display_metadata says about why
// the turn failed.
type storedFailure struct {
	Error   string `json:"error"`
	Surface struct {
		Layer    string `json:"layer"`
		Provider string `json:"provider"`
		AuthKind string `json:"auth_kind"`
	} `json:"error_surface"`
}

// parseFailure reads a failed-turn row's display_metadata; ok is false when
// it carries no error_surface.
func parseFailure(meta string) (storedFailure, bool) {
	var f storedFailure
	if meta == "" || json.Unmarshal([]byte(meta), &f) != nil || f.Surface.Layer == "" {
		return storedFailure{}, false
	}
	return f, true
}

// authFailed reports whether the turn failed because the login was refused:
// hermes put the failure on its auth layer (error_surface.layer "auth",
// agent/error_surface.py) AND the provider answered HTTP 401 — the error is
// _summarize_api_error's text, "HTTP <status>: …"
// (agent/api_error_summary.py _http_prefix). hermes's auth layer also takes
// 403s (agent/error_classifier.py _status_403), which are not: the one real
// failure in Drew's store is a copilot 403, "HTTP 403: unauthorized: not
// licensed to use Copilot" — a subscription, which signing in again would
// not fix.
func (f storedFailure) authFailed() bool {
	return f.Surface.Layer == "auth" && strings.HasPrefix(f.Error, "HTTP 401: ")
}

// failedTurnErrorKind classifies a failed-turn row for Message.ErrorKind.
func failedTurnErrorKind(meta string) string {
	if f, ok := parseFailure(meta); ok && f.authFailed() {
		return provider.ErrorKindAuth
	}
	return ""
}

// reloginProviders are the inference providers `hermes auth add <slug>
// --type oauth` can sign in: nous (_add_nous_oauth_credential) and the OAuth
// specs (_OAUTH_ADD_SPECS), hermes_cli/auth_commands.py. They are the
// commands hermes itself prints for a refused OAuth grant
// (agent/turn_failure_copy.py oauth_relogin_command), except that for nous
// it prints `hermes portal`, which is that same login plus onboarding (a
// model pick, setting Nous as the provider, the Tool Gateway offer).
var reloginProviders = []string{"nous", "anthropic", "openai-codex", "xai-oauth", "qwen-oauth", "minimax-oauth", "openrouter"}

// LoginCommand is the sign-in for the login account names
// (provider.Authenticator). A hermes home pools credentials for several
// inference providers, so the command is the one that re-signs the provider
// whose refusal signed the login out: the newest 401 auth failure in the
// home's state.db. Only an OAuth provider gets one — `hermes auth add
// <provider> --type oauth`, which opens the browser (or prints a device
// code) and needs no other prompt. An API key refused (auth_kind
// "api_key") is fixed by a new key, not a sign-in, and copilot's key may
// come from `gh` itself; for those, and for a home with no such failure, no
// command. The home goes in as HERMES_HOME only when it isn't the default.
// argv[0] is the `hermes` CLI beside the hermes-acp sessions spawn, and the
// command is withheld when launching it would start hermes's dependency
// install (pkg/hermesinstall).
func (p *Provider) LoginCommand(account string) (provider.LoginCommand, bool) {
	dir, ok := strings.CutPrefix(account, accountPrefix)
	if !ok || !filepath.IsAbs(dir) {
		return provider.LoginCommand{}, false
	}
	bin := p.cliBin()
	if bin == "" {
		return provider.LoginCommand{}, false
	}
	f, ok := lastAuthFailure(context.Background(), dir)
	if !ok || f.Surface.AuthKind != "oauth" || !slices.Contains(reloginProviders, f.Surface.Provider) {
		return provider.LoginCommand{}, false
	}
	cmd := provider.LoginCommand{Argv: []string{bin, "auth", "add", f.Surface.Provider, "--type", "oauth"}, Env: []string{}}
	if accountKey(withoutHermesHome(p.childEnv())) != account {
		cmd.Env = append(cmd.Env, "HERMES_HOME="+dir)
	}
	if hermesinstall.Check(bin, append(os.Environ(), "HERMES_HOME="+dir)) != nil {
		return provider.LoginCommand{}, false
	}
	return cmd, true
}

// cliBin is the `hermes` CLI installed beside the hermes-acp sessions spawn
// (Config.Bin, else binlocate.Find, which honours $MADS_AGENT_BIN_DIR):
// hermes's installer links both into one directory (setup-hermes.sh:216).
// "" when there is no absolute, executable one.
func (p *Provider) cliBin() string {
	acp := p.cfg.Bin
	if acp == "" {
		acp, _ = binlocate.Find("hermes-acp")
	}
	if !filepath.IsAbs(acp) {
		return ""
	}
	bin := filepath.Join(filepath.Dir(acp), "hermes")
	if fi, err := os.Stat(bin); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return ""
	}
	return bin
}

// withoutHermesHome is env with any HERMES_HOME dropped: the environment of
// a hermes on its default home.
func withoutHermesHome(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		return strings.HasPrefix(kv, "HERMES_HOME=")
	})
}

// lastAuthFailure is the newest failed turn in home's state.db that failed
// for auth (authFailed). It reads failed-turn rows only, newest first, and
// stops at the first auth failure.
func lastAuthFailure(ctx context.Context, home string) (storedFailure, bool) {
	path := filepath.Join(home, "state.db")
	if _, err := os.Stat(path); err != nil {
		return storedFailure{}, false
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?mode=ro&_pragma=busy_timeout(2000)&_pragma=query_only(1)")
	if err != nil {
		return storedFailure{}, false
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, `SELECT display_metadata FROM messages
		WHERE display_kind = ? AND display_metadata IS NOT NULL ORDER BY id DESC`, failedTurnKind)
	if err != nil {
		return storedFailure{}, false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var meta string
		if rows.Scan(&meta) != nil {
			continue
		}
		if f, ok := parseFailure(meta); ok && f.authFailed() {
			return f, true
		}
	}
	return storedFailure{}, false
}

// errNoAuthFailure: the turn that just ended left no auth failure behind.
var errNoAuthFailure = errors.New("no auth failure")

// turnAuthFailure is the EventError for a turn that hermes ended by
// refusing it for auth: the newest display row of ref is a failed-turn
// boundary written at or after since (unix seconds, the turn's send) whose
// failure is an auth failure. hermes writes that row before it answers the
// prompt, so it is already there when the turn ends.
func (p *Provider) turnAuthFailure(ctx context.Context, ref string, since float64, account string) (provider.Event, error) {
	rows, err := p.readRows(ctx, ref)
	if err != nil {
		return provider.Event{}, err
	}
	if len(rows) == 0 {
		return provider.Event{}, errNoAuthFailure
	}
	last := rows[len(rows)-1]
	if last.role != "assistant" || !failedTurn(last.displayKind, decodeContent(last.content)) || last.timestamp < since {
		return provider.Event{}, errNoAuthFailure
	}
	f, ok := parseFailure(last.displayMeta)
	if !ok || !f.authFailed() {
		return provider.Event{}, errNoAuthFailure
	}
	ev := provider.Event{Kind: provider.EventError, Text: f.Error, ErrorKind: provider.ErrorKindAuth}
	if account != "" {
		ev.Account = &provider.AccountStatus{Account: account}
	}
	return ev, nil
}
