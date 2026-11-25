//go:build mesh_loopback

package meshnet

// Built with -tags mesh_loopback (the Playwright harness's Router, whose
// stand-in devices listen on 127.0.0.1): -mesh-allow-loopback is allowed.
func init() { loopbackBuild = true }
