package runner

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// authTally is what the runner knows about the login its session runs on
// (docs/AUTH-SIGNIN-DESIGN.md): the account key the harness last reported,
// for the auth reports whose event doesn't name it.
type authTally struct {
	mu      sync.Mutex
	account string
}

// observeAuth reports the login's sign-in state as the provider reveals it:
// a turn refused for auth signs the login out; a turn that got a real
// answer (token usage, so the API took the request) shows it works, which
// clears a sign-out another session on the same login reported. The store
// ignores the second on a login that isn't signed out, so reporting every
// answered turn costs a round trip and nothing else.
func (r *Runner) observeAuth(ev provider.Event) {
	switch {
	case ev.Kind == provider.EventAccount && ev.Account != nil && ev.Account.Account != "":
		r.auth.mu.Lock()
		r.auth.account = ev.Account.Account
		r.auth.mu.Unlock()
	case ev.Kind == provider.EventError && ev.ErrorKind == provider.ErrorKindAuth:
		account := ""
		if ev.Account != nil {
			account = ev.Account.Account
		}
		r.reportAuthFailed(account)
	case ev.Kind == provider.EventTurnEnd && ev.Usage != nil && !tokenCounts(ev.Usage).IsZero():
		r.reportAuthOK()
	}
}

// authAccount is the login to report against: the event's own, else the
// last one the harness reported, else the provider's default login. Empty
// when none of them can say.
func (r *Runner) authAccount(fromEvent string) (account, providerName string) {
	r.mu.Lock()
	prov := r.prov
	providerName = r.providerName
	r.mu.Unlock()
	if fromEvent != "" {
		return fromEvent, providerName
	}
	r.auth.mu.Lock()
	account = r.auth.account
	r.auth.mu.Unlock()
	if account == "" {
		if k, ok := prov.(provider.AccountKeyer); ok {
			account = k.DefaultAccount()
		}
	}
	return account, providerName
}

func (r *Runner) reportAuthFailed(fromEvent string) {
	if r.data == nil {
		return
	}
	account, providerName := r.authAccount(fromEvent)
	if account == "" {
		log.Printf("runner: auth failure on an unknown login: not reported")
		return
	}
	err := r.data.AccountAuthFailed(context.Background(), contracts.AccountAuthFailed{
		Account: account, Provider: providerName, SessionID: r.sessionID, At: time.Now().UTC(),
	})
	if err != nil {
		log.Printf("runner: report auth failure: %v", err)
	}
}

func (r *Runner) reportAuthOK() {
	if r.data == nil {
		return
	}
	account, _ := r.authAccount("")
	if account == "" {
		return
	}
	if err := r.data.AccountAuthOK(context.Background(), contracts.AccountAuthOK{Account: account, At: time.Now().UTC()}); err != nil {
		log.Printf("runner: report auth ok: %v", err)
	}
}
