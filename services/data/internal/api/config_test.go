package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestConfigRoutes(t *testing.T) {
	srv, _ := newTestServer(t)

	var cfg contracts.Config
	if resp := doJSON(t, srv, "GET", "/config", nil, &cfg); resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status %d", resp.StatusCode)
	}
	if !reflect.DeepEqual(cfg, contracts.DefaultConfig()) {
		t.Fatalf("fresh config = %+v, want DefaultConfig()", cfg)
	}

	cfg.Palette = "glacier"
	cfg.ConcurrencyLimit = 8
	var updated contracts.Config
	if resp := doJSON(t, srv, "PUT", "/config", cfg, &updated); resp.StatusCode != http.StatusOK {
		t.Fatalf("put: status %d", resp.StatusCode)
	}
	if updated.Palette != "glacier" || updated.ConcurrencyLimit != 8 {
		t.Fatalf("put did not echo update: %+v", updated)
	}

	var reread contracts.Config
	doJSON(t, srv, "GET", "/config", nil, &reread)
	if reread.Palette != "glacier" {
		t.Fatalf("update did not persist: %+v", reread)
	}
}

func TestConfigRoutesValidateAll(t *testing.T) {
	srv, _ := newTestServer(t)

	bad := contracts.DefaultConfig()
	bad.Theme = "invalid"
	bad.ConcurrencyLimit = -1

	var verr contracts.ValidationError
	resp := doJSON(t, srv, "PUT", "/config", bad, &verr)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if len(verr.Errors) < 2 {
		t.Fatalf("want every problem reported at once, got %+v", verr.Errors)
	}
}

func TestProfileRoutesAndActivate(t *testing.T) {
	srv, _ := newTestServer(t)

	tierJSON, err := json.Marshal(contracts.ModelTierBest)
	if err != nil {
		t.Fatal(err)
	}
	var p contracts.Profile
	resp := doJSON(t, srv, "POST", "/profiles", contracts.Profile{
		Name:     "night-owl",
		Snapshot: contracts.ProfileSnapshot{"default_model_tier": tierJSON},
	}, &p)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}

	var list []contracts.Profile
	doJSON(t, srv, "GET", "/profiles", nil, &list)
	if len(list) != 1 {
		t.Fatalf("list = %+v, want 1 profile", list)
	}

	var merged contracts.Config
	resp = doJSON(t, srv, "POST", "/profiles/"+p.ID+"/activate", nil, &merged)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activate: status %d", resp.StatusCode)
	}
	if merged.DefaultModelTier != contracts.ModelTierBest {
		t.Fatalf("activate did not merge: %+v", merged)
	}

	if resp := doJSON(t, srv, "DELETE", "/profiles/"+p.ID, nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}
}
