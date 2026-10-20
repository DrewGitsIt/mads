package main

import "testing"

// TestResolveProviderGoose pins the -provider switch: "goose" resolves to
// the goosecli adapter (registry name "goose"), with an empty -goose-args
// default — full trust rides ACP session mode "auto", set by the engine.
func TestResolveProviderGoose(t *testing.T) {
	prov, err := resolveProvider("goose", "--dangerously-skip-permissions", "--trust-all-tools", "", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if prov.Name() != "goose" {
		t.Fatalf("Name() = %q, want %q", prov.Name(), "goose")
	}
}

// TestResolveProviderHermes pins "hermes" to the hermescli adapter with no
// args of its own (hermes-acp has none to take).
func TestResolveProviderHermes(t *testing.T) {
	prov, err := resolveProvider("hermes", "--dangerously-skip-permissions", "--trust-all-tools", "", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if prov.Name() != "hermes" {
		t.Fatalf("Name() = %q, want %q", prov.Name(), "hermes")
	}
}

func TestResolveProviderUnknown(t *testing.T) {
	if _, err := resolveProvider("no-such-provider", "", "", "", "", t.TempDir()); err == nil {
		t.Fatal("resolveProvider(no-such-provider) = nil error, want unknown-provider error")
	}
}
