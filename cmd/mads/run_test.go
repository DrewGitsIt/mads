package main

import "testing"

// TestResolveProviderGoose pins the -provider switch: "goose" resolves to
// the goosecli adapter (registry name "goose"), with an empty -goose-args
// default — full trust rides ACP session mode "auto", set by the engine.
func TestResolveProviderGoose(t *testing.T) {
	prov, err := resolveProvider("goose", "--dangerously-skip-permissions", "--trust-all-tools", "", "", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if prov.Name() != "goose" {
		t.Fatalf("Name() = %q, want %q", prov.Name(), "goose")
	}
}

func TestResolveProviderUnknown(t *testing.T) {
	if _, err := resolveProvider("no-such-provider", "", "", "", "", "", t.TempDir()); err == nil {
		t.Fatal("resolveProvider(no-such-provider) = nil error, want unknown-provider error")
	}
}

// TestResolveProviderCodex pins the -provider switch for the codex adapter
// (registry name "codex", shown as "Codex (ChatGPT)").
func TestResolveProviderCodex(t *testing.T) {
	prov, err := resolveProvider("codex", "--dangerously-skip-permissions", "--trust-all-tools", "", "-c model_reasoning_effort=\"low\"", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if prov.Name() != "codex" {
		t.Fatalf("Name() = %q, want %q", prov.Name(), "codex")
	}
}
