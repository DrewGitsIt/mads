package api

import "time"

// SetPermissionRequestsChildTimeout shortens the per-child bound of
// GET /permission-requests for a test; the returned func restores it.
func SetPermissionRequestsChildTimeout(d time.Duration) (restore func()) {
	prev := permissionRequestsChildTimeout
	permissionRequestsChildTimeout = d
	return func() { permissionRequestsChildTimeout = prev }
}
