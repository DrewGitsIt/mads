package acprpc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

func TestDiscoverModelsListsAndDefault(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	models, err := e.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	want := []provider.Model{
		{ID: "fake-default", Name: "Fake Default", Default: true},
		{ID: "fake-alt", Name: "Fake Alt"},
	}
	if len(models) != len(want) {
		t.Fatalf("models = %+v, want %+v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("models[%d] = %+v, want %+v", i, models[i], want[i])
		}
	}
}

func TestDiscoverModelsMissingBinary(t *testing.T) {
	cfg := gooseConfig()
	cfg.Bin = filepath.Join(t.TempDir(), "no-such-agent")
	cfg.SpawnArgs = []string{"acp"}
	_, err := New(cfg).DiscoverModels(context.Background())
	if !errors.Is(err, provider.ErrDiscoveryUnavailable) {
		t.Fatalf("err = %v, want ErrDiscoveryUnavailable", err)
	}
}

func TestDiscoverModelsNoModelOptionID(t *testing.T) {
	cfg := gooseConfig()
	cfg.ModelConfigOptionID = ""
	_, err := testEngine(t, cfg, "goose", t.TempDir()).DiscoverModels(context.Background())
	if !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
