package childsock

import (
	"log"
	"net"
)

// ListenChild is a child service's listener: the Unix socket the Router
// passed as -socket, or — only when a person runs the service by hand
// without one — TCP on addr, with a loud warning, since a TCP port has no
// same-user check and any local user can reach it.
func ListenChild(service, socket, addr string) (net.Listener, error) {
	if socket != "" {
		return Listen(socket)
	}
	log.Printf("%s: WARNING: listening on TCP %s with no same-user check — any local user can reach this port. "+
		"That is only for running %s by hand; the Router always passes -socket.", service, addr, service)
	return net.Listen("tcp", addr)
}
