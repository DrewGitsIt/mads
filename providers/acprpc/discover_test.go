package acprpc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// The probe's session is an artifact of asking a question, so it must not
// survive as a user-visible session. session/close alone leaves it
// persisted: goose had accumulated 1,011 rows in sessions.db from earlier
// discovery probes before this was fixed, and a per-boot warm would add
// one every startup.
func TestDiscoverModelsDeletesItsThrowawaySession(t *testing.T) {
	dir := t.TempDir()
	if _, err := testEngine(t, gooseConfig(), "goose", dir).DiscoverModels(context.Background()); err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "delete-called")); err != nil {
		t.Errorf("probe did not send session/delete: %v", err)
	}
}

// An agent that never advertised sessionCapabilities.delete must not be
// sent the call at all — the same capability gate session/close rides.
func TestDiscoverModelsWithholdsDeleteWithoutCapability(t *testing.T) {
	dir := t.TempDir()
	cfg := gooseConfig()
	cfg.ModelConfigOptionID = ""
	if _, err := testEngine(t, cfg, "selector", dir).DiscoverModels(context.Background()); err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "delete-called")); err == nil {
		t.Error("sent session/delete to an agent that never advertised it")
	}
}

// An agent carrying only ACP's `models` selector — hermes ships 70 models
// this way with no configOptions at all — must be discoverable. Reading
// just the configOption made it look model-less.
func TestDiscoverModelsFromSelectorOnly(t *testing.T) {
	cfg := gooseConfig()
	cfg.ModelConfigOptionID = ""
	models, err := testEngine(t, cfg, "selector", t.TempDir()).DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	want := []provider.Model{
		{ID: "sel-a", Name: "Selector A"},
		{ID: "sel-b", Name: "Selector B", Default: true},
		// No name on the wire: the id is the only label the UI can show.
		{ID: "sel-c", Name: "sel-c"},
	}
	if !slices.Equal(models, want) {
		t.Fatalf("models = %+v, want %+v", models, want)
	}
}

// Where an agent ships both surfaces (codex), the selector wins: it is the
// finer-grained list and the only one that names the live selection.
func TestDiscoverModelsPrefersSelectorOverConfigOption(t *testing.T) {
	models, err := testEngine(t, gooseConfig(), "dual", t.TempDir()).DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	for _, m := range models {
		if m.ID == "fake-default" || m.ID == "fake-alt" {
			t.Fatalf("models = %+v, want the selector's ids, not the configOption's", models)
		}
	}
	if len(models) != 3 || models[1].ID != "sel-b" || !models[1].Default {
		t.Fatalf("models = %+v, want the 3 selector entries with sel-b current", models)
	}
}

// An agent with neither surface is a different fact from one offering an
// empty list, and the distinction has to survive to the caller: silently
// returning no models is what hid hermes.
func TestDiscoverModelsNoSurfaceAtAll(t *testing.T) {
	_, err := testEngine(t, gooseConfig(), "bare", t.TempDir()).DiscoverModels(context.Background())
	if !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// Selection must use the setter matching the surface discovery read from,
// or the picker offers ids that cannot be selected. The fake rejects a
// configOption value arriving at session/set_model and vice versa.
func TestStartSelectsModelViaSetModel(t *testing.T) {
	cfg := gooseConfig()
	cfg.ModelConfigOptionID = ""
	e := testEngine(t, cfg, "selector", t.TempDir())
	sess, err := e.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, Cwd: t.TempDir(), Trust: provider.TrustFull, Model: "sel-a",
	})
	if err != nil {
		t.Fatalf("Start with a selector model: %v", err)
	}
	_ = sess.Close()
}

// The configOption path is unchanged for agents that only offer it — goose
// is the shipped one, and it must not start riding session/set_model.
func TestStartSelectsModelViaConfigOption(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	sess, err := e.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, Cwd: t.TempDir(), Trust: provider.TrustFull, Model: "fake-alt",
	})
	if err != nil {
		t.Fatalf("Start with a configOption model: %v", err)
	}
	_ = sess.Close()
}
