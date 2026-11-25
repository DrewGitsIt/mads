// Package tailnet is the Mesh Service's view of tailscaled: who this node
// is, and who is calling (docs/MESH-MODE-DESIGN.md D2). It's
// pkg/meshnet/tailscale under the names the Mesh Service has always used;
// that package finds the LocalAPI on Linux and on macOS (the Tailscale
// app's port and token, or the open-source daemon's socket).
package tailnet

import "github.com/drewkidwell/mads/pkg/meshnet/tailscale"

// DefaultSocket is tailscaled's LocalAPI socket on Linux.
const DefaultSocket = tailscale.LinuxSocket

// MeshCap is the tailnet app capability earlier builds required; it's no
// longer read for admission.
const MeshCap = tailscale.MeshCap

type (
	// Client talks to one tailscaled LocalAPI.
	Client = tailscale.Client
	// Self is this node as tailscaled sees it.
	Self = tailscale.Self
	// Caller is the tailnet identity behind a remote address.
	Caller = tailscale.Caller
)

// ErrNotFound is WhoIs on an address that isn't a tailnet peer.
var ErrNotFound = tailscale.ErrNotPeer

// New returns a Client for the LocalAPI socket at path.
func New(path string) *Client { return tailscale.NewSocket(path) }

// Locate returns a Client for wherever this machine's tailscaled serves
// its LocalAPI (socket "" finds it; a path uses only that socket).
func Locate(socket string) *Client { return tailscale.New(tailscale.Locator{Socket: socket}) }
