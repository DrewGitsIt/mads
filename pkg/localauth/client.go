package localauth

import (
	"errors"
	"net/http"
	"os"
)

// SetBearer adds tok to req as `Authorization: Bearer`, unless tok is empty.
func SetBearer(req *http.Request, tok string) {
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// Transport is an http.RoundTripper that sends the token from Source on
// every request.
type Transport struct {
	Base   http.RoundTripper
	Source func() string
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if t.Source == nil {
		return base.RoundTrip(req)
	}
	tok := t.Source()
	if tok == "" || req.Header.Get("Authorization") != "" {
		return base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	SetBearer(clone, tok)
	return base.RoundTrip(clone)
}

// ClientToken is the install token for a CLI-style caller: read from
// madsHome, or "" when there is none yet (the server's 401 then says what
// to do). An unsafe file is an error, not silently skipped.
func ClientToken(madsHome string) (string, error) {
	tok, err := ReadToken(madsHome)
	if errors.Is(err, ErrNoToken) {
		return "", nil
	}
	return tok, err
}

// TakeChildEnv removes MADS_LOCAL_TOKEN and MADS_ROUTER_TOKEN from this
// process's environment and returns their values. A Session Service or
// Pipeline Engine child calls it first thing in main, so nothing it starts
// afterwards (an agent, git, an MCP server) can inherit either.
func TakeChildEnv() (localToken, routerToken string) {
	localToken = os.Getenv(EnvLocalToken)
	routerToken = os.Getenv(EnvRouterToken)
	_ = os.Unsetenv(EnvLocalToken)
	_ = os.Unsetenv(EnvRouterToken)
	return localToken, routerToken
}

// ScrubEnv returns env without either token variable — the last line of
// defense in pkg/agentenv, whatever the parent process holds.
func ScrubEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if IsTokenVar(kv) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// IsTokenVar reports whether a KEY=VALUE entry is one of the token
// variables.
func IsTokenVar(kv string) bool {
	for _, name := range []string{EnvLocalToken, EnvRouterToken} {
		if len(kv) > len(name) && kv[:len(name)] == name && kv[len(name)] == '=' {
			return true
		}
		if kv == name {
			return true
		}
	}
	return false
}
