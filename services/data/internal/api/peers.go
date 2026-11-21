package api

import (
	"context"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// writeContext is a write's context for the store: marked with host
// authority (store.WithHostAuthority) when this host's own user made it
// directly — the install token, and no mesh-relay marker. The Router sets
// the marker (contracts.MeshRelayHeader) on every Data Service write it
// makes for a request a peer's Mesh Service forwarded, so a mesh caller
// never gets host authority, whatever route a future change opens. The
// marker's presence is what counts, even empty: the Mesh Service stamps
// the caller's node name, which may be blank.
func writeContext(req *http.Request) context.Context {
	ctx := req.Context()
	if _, relayed := req.Header[contracts.MeshRelayHeader]; relayed {
		// Never host authority; and the peer registry holds it to what the
		// relaying device (an admin there) may do remotely.
		return store.WithMeshRelay(ctx, req.Header.Get(contracts.MeshDeviceHeader))
	}
	if localauth.FromContext(ctx).Kind != localauth.Install {
		return ctx
	}
	return store.WithHostAuthority(ctx)
}
