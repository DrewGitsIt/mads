package contracts

// Model mirrors providers/provider/provider.go Model — one selectable
// model, duplicated here (not imported) so pkg/contracts stays free of a
// dependency on the provider layer, same as every other cross-boundary type
// in this package.
type Model struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default,omitempty"`
}

// ModelsResponse is GET /providers/{name}/models' wire shape. Models is
// always a non-nil (possibly empty) array, never null. Detail is populated
// only when discovery failed in an expected way (provider.
// ErrDiscoveryUnavailable) and Models is consequently empty — its presence
// is the UI's signal to render "no models" instead of treating an empty
// list as a real, discoverable-but-empty provider.
// State distinguishes the three ways Models can be empty, which the UI
// renders differently: "refreshing" (discovery still running, show a
// spinner), "unavailable" (paired with Detail — show the reason), and
// "ready" (the provider genuinely has no models). Empty when the Router
// answered without a catalog cache. FetchedAt is RFC3339, empty when never
// fetched.
type ModelsResponse struct {
	Models    []Model `json:"models"`
	Detail    string  `json:"detail,omitempty"`
	State     string  `json:"state,omitempty"`
	FetchedAt string  `json:"fetched_at,omitempty"`
}
