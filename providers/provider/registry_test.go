package provider

import (
	"context"
	"strings"
	"testing"
)

// namedProvider is the minimal Provider for registry tests.
type namedProvider struct {
	name   string
	matrix Matrix
}

func (p namedProvider) Name() string   { return p.name }
func (p namedProvider) Matrix() Matrix { return p.matrix }
func (p namedProvider) Start(context.Context, StartOptions) (Session, error) {
	return nil, ErrUnsupported
}
func (p namedProvider) OneShot(context.Context, OneShotRequest) (*OneShotResult, error) {
	return nil, ErrUnsupported
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	ok := namedProvider{name: "fake", matrix: fullMatrix(nil)}
	if err := r.Register(ok); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(ok); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("duplicate registration: %v", err)
	}
	if err := r.Register(namedProvider{name: "", matrix: fullMatrix(nil)}); err == nil {
		t.Error("empty name accepted")
	}

	// An invalid matrix never becomes visible (the registration guard).
	bad := namedProvider{name: "broken", matrix: fullMatrix(map[Behavior]Support{
		BehaviorOneShot: Unsupported,
	})}
	if err := r.Register(bad); err == nil || !strings.Contains(err.Error(), "invalid capability matrix") {
		t.Errorf("invalid matrix accepted: %v", err)
	}
	if _, err := r.Get("broken"); err == nil {
		t.Error("invalid provider is visible")
	}

	got, err := r.Get("fake")
	if err != nil || got.Name() != "fake" {
		t.Errorf("Get: %v, %v", got, err)
	}
	if _, err := r.Get("nope"); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("unknown lookup: %v", err)
	}
	if names := r.Names(); len(names) != 1 || names[0] != "fake" {
		t.Errorf("Names: %v", names)
	}
}
