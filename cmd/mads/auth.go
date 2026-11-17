package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/localauth"
)

// installToken is the install token every subcommand sends
// (docs/LOCAL-FRONT-DOOR-DESIGN.md D5), read from $MADS_HOME/local.token.
// No token yet is "" — the service's 401 then says what to do. A token file
// other users could read is fatal: the CLI won't use (and so spread) it.
func installToken() string {
	tok, err := loadInstallToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mads:", err)
		os.Exit(1)
	}
	return tok
}

// loadInstallToken is installToken returning the error instead of exiting.
func loadInstallToken() (string, error) {
	return localauth.ClientToken(localauth.DefaultHome())
}

// dataClient is dataclient.New with the install token.
func dataClient(base, actor string) *dataclient.Client {
	return dataclient.New(base, actor, dataclient.WithToken(installToken()))
}

// routerClient is an http.Client that sends the install token, for calls to
// the Router.
func routerClient() *http.Client {
	tok := installToken()
	return &http.Client{Transport: &localauth.Transport{Source: func() string { return tok }}}
}
