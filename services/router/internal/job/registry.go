package job

import (
	"context"
	"errors"
	"fmt"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
)

const (
	RegistryInstallKind = "registry_install"
	RegistryUpdateKind  = "registry_update"
	RegistryPublishKind = "registry_publish"
)

// RegistryInstall fetches one bundle and upserts it into the local registry
// by name — used for both the Install and Update entry points (Update is
// just Install against an item that already exists locally with a
// different version picked; the upsert logic is identical, only the job
// Kind string passed to Spawn differs, for activity-feed labeling).
func RegistryInstall(itemType registryclient.ItemType, name, version string) Func {
	return func(ctx context.Context, deps Deps) error {
		bundle, err := deps.Registry.Bundle(ctx, itemType, name, version)
		if err != nil {
			return fmt.Errorf("registry install: fetch %s %q@%s: %w", itemType, name, version, err)
		}
		if verr := bundle.Validate(); verr != nil {
			return fmt.Errorf("registry install: malformed bundle: %w", verr)
		}
		switch itemType {
		case registryclient.ItemAgent:
			return upsertAgent(ctx, deps.Data, bundle.Agent)
		case registryclient.ItemCapability:
			return upsertCapability(ctx, deps.Data, bundle.Capability)
		case registryclient.ItemTemplate:
			return upsertTemplate(ctx, deps.Data, bundle.Template)
		default:
			return fmt.Errorf("registry install: unknown item type %q", itemType)
		}
	}
}

// PublishItem names one locally-owned item to publish (checked in the
// Publish dialog).
type PublishItem struct {
	Type registryclient.ItemType
	ID   string
}

// RegistryPublish loads each named local item and publishes it as a new
// bundle version, in order, stopping at the first failure — a partial
// publish reports exactly which item failed via job.failed's error string
// rather than silently skipping it.
func RegistryPublish(items []PublishItem) Func {
	return func(ctx context.Context, deps Deps) error {
		for _, it := range items {
			bundle, err := loadBundle(ctx, deps.Data, it.Type, it.ID)
			if err != nil {
				return fmt.Errorf("registry publish: load %s %s: %w", it.Type, it.ID, err)
			}
			if _, err := deps.Registry.Publish(ctx, bundle); err != nil {
				return fmt.Errorf("registry publish: %s %q: %w", it.Type, bundle.Name, err)
			}
		}
		return nil
	}
}

func loadBundle(ctx context.Context, data *dataclient.Client, t registryclient.ItemType, id string) (registryclient.Bundle, error) {
	switch t {
	case registryclient.ItemAgent:
		a, err := data.Agent(ctx, id)
		return registryclient.Bundle{Type: t, Agent: &a}, err
	case registryclient.ItemCapability:
		c, err := data.Capability(ctx, id)
		return registryclient.Bundle{Type: t, Capability: &c}, err
	case registryclient.ItemTemplate:
		tpl, err := data.Template(ctx, id)
		return registryclient.Bundle{Type: t, Template: &tpl}, err
	default:
		return registryclient.Bundle{}, fmt.Errorf("registry publish: unknown item type %q", t)
	}
}

// upsertAgent/upsertCapability/upsertTemplate look the installed item up by
// name first (the registry lookup key) — ErrNotFound means Create,
// otherwise Update carrying the existing local ID forward, so a
// re-install/update never orphans local references (pipeline `agent:`
// bindings, workspace template bindings) that point at the ID.
func upsertAgent(ctx context.Context, data *dataclient.Client, a *contracts.Agent) error {
	existing, err := data.Agent(ctx, a.Name)
	if err == nil {
		a.ID = existing.ID
		return data.UpdateAgent(ctx, a)
	}
	if !errors.Is(err, dataclient.ErrNotFound) {
		return err
	}
	return data.CreateAgent(ctx, a)
}

func upsertCapability(ctx context.Context, data *dataclient.Client, c *contracts.Capability) error {
	existing, err := data.Capability(ctx, c.Name)
	if err == nil {
		c.ID = existing.ID
		return data.UpdateCapability(ctx, c)
	}
	if !errors.Is(err, dataclient.ErrNotFound) {
		return err
	}
	return data.CreateCapability(ctx, c)
}

func upsertTemplate(ctx context.Context, data *dataclient.Client, t *contracts.Template) error {
	existing, err := data.Template(ctx, t.Name)
	if err == nil {
		t.ID = existing.ID
		return data.UpdateTemplate(ctx, t)
	}
	if !errors.Is(err, dataclient.ErrNotFound) {
		return err
	}
	return data.CreateTemplate(ctx, t)
}
