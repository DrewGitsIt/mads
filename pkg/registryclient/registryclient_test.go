package registryclient_test

import (
	"context"
	"errors"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/pkg/registrytest"
)

func TestClientRoundTrip(t *testing.T) {
	base := registrytest.Start(t)
	client := registryclient.New(base)
	ctx := context.Background()

	published, err := client.Publish(ctx, registryclient.Bundle{
		Type: registryclient.ItemAgent,
		Agent: &contracts.Agent{
			Name:        "code-reviewer",
			Description: "reviews diffs",
			Prompt:      "review this diff",
		},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Version != "1" {
		t.Fatalf("expected version 1, got %q", published.Version)
	}

	// Publish a second version of the same agent.
	published2, err := client.Publish(ctx, registryclient.Bundle{
		Type: registryclient.ItemAgent,
		Agent: &contracts.Agent{
			Name:        "code-reviewer",
			Description: "reviews diffs, v2",
			Prompt:      "review this diff carefully",
		},
	})
	if err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	if published2.Version != "2" {
		t.Fatalf("expected version 2, got %q", published2.Version)
	}

	entries, err := client.Index(ctx, "")
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Latest != "2" {
		t.Fatalf("expected latest 2, got %q", entries[0].Latest)
	}
	if len(entries[0].Versions) != 2 {
		t.Fatalf("expected 2 versions, got %v", entries[0].Versions)
	}

	// Search matches name substring, case-insensitive.
	found, err := client.Index(ctx, "CODE-rev")
	if err != nil {
		t.Fatalf("index search by name: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 match for name search, got %d", len(found))
	}

	// Search matches description substring, case-insensitive.
	foundDesc, err := client.Index(ctx, "DIFFS, V2")
	if err != nil {
		t.Fatalf("index search by description: %v", err)
	}
	if len(foundDesc) != 1 {
		t.Fatalf("expected 1 match for description search, got %d", len(foundDesc))
	}

	noMatch, err := client.Index(ctx, "no-such-thing")
	if err != nil {
		t.Fatalf("index search no match: %v", err)
	}
	if len(noMatch) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(noMatch))
	}

	// Fetch by exact version.
	got, err := client.Bundle(ctx, registryclient.ItemAgent, "code-reviewer", "1")
	if err != nil {
		t.Fatalf("bundle by version: %v", err)
	}
	if got.Agent == nil || got.Agent.Description != "reviews diffs" {
		t.Fatalf("expected v1 payload, got %+v", got)
	}

	// Fetch latest.
	latest, err := client.Bundle(ctx, registryclient.ItemAgent, "code-reviewer", "latest")
	if err != nil {
		t.Fatalf("bundle latest: %v", err)
	}
	if latest.Agent == nil || latest.Agent.Description != "reviews diffs, v2" {
		t.Fatalf("expected v2 payload, got %+v", latest)
	}

	// Unknown name -> ErrNotFound.
	if _, err := client.Bundle(ctx, registryclient.ItemAgent, "no-such-agent", "latest"); !errors.Is(err, registryclient.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unknown name, got %v", err)
	}

	// Unknown version -> ErrNotFound.
	if _, err := client.Bundle(ctx, registryclient.ItemAgent, "code-reviewer", "99"); !errors.Is(err, registryclient.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unknown version, got %v", err)
	}
}

func TestClientNotConfigured(t *testing.T) {
	client := registryclient.New("")
	if client.Configured() {
		t.Fatal("expected Configured() false for empty base")
	}
	if _, err := client.Index(context.Background(), ""); err == nil {
		t.Fatal("expected error for unconfigured client")
	}
}

func TestBundleValidate(t *testing.T) {
	cases := []struct {
		name    string
		bundle  registryclient.Bundle
		wantErr bool
	}{
		{
			name:    "valid agent",
			bundle:  registryclient.Bundle{Type: registryclient.ItemAgent, Agent: &contracts.Agent{Name: "a", Prompt: "p"}},
			wantErr: false,
		},
		{
			name:    "valid capability",
			bundle:  registryclient.Bundle{Type: registryclient.ItemCapability, Capability: &contracts.Capability{Name: "c"}},
			wantErr: false,
		},
		{
			name:    "valid template",
			bundle:  registryclient.Bundle{Type: registryclient.ItemTemplate, Template: &contracts.Template{Name: "t"}},
			wantErr: false,
		},
		{
			name:    "empty type",
			bundle:  registryclient.Bundle{},
			wantErr: true,
		},
		{
			name:    "type/payload mismatch",
			bundle:  registryclient.Bundle{Type: registryclient.ItemAgent, Capability: &contracts.Capability{Name: "c"}},
			wantErr: true,
		},
		{
			name: "two payloads set",
			bundle: registryclient.Bundle{
				Type:       registryclient.ItemAgent,
				Agent:      &contracts.Agent{Name: "a", Prompt: "p"},
				Capability: &contracts.Capability{Name: "c"},
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verr := tc.bundle.Validate()
			if tc.wantErr && verr == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !tc.wantErr && verr != nil {
				t.Fatalf("expected no validation error, got %v", verr)
			}
		})
	}
}
