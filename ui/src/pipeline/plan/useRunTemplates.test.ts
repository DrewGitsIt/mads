import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { DEFAULT_TEMPLATE_ID, useRunTemplates } from "./useRunTemplates";
import { FakeRouterClient, fixtureWorkspaces } from "../../router/fixtures";
import { FakeEventSource } from "../../router/testFakeEventSource";
import type { Template, Workspace } from "../../router/types";

function template(id: string, name: string): Template {
  return { id, name, source: "", version: 1, created_at: "t", updated_at: "t" };
}

const templates = [
  template(DEFAULT_TEMPLATE_ID, "default-implementation"),
  template("tpl_fast", "fast-path"),
];

class TemplatesClient extends FakeRouterClient {
  constructor(private readonly bound?: string) {
    super();
  }

  override async listTemplates(): Promise<Template[]> {
    return templates;
  }

  override async listWorkspaces(): Promise<Workspace[]> {
    return [{ ...fixtureWorkspaces[0], defaults: { template: this.bound } }];
  }
}

describe("useRunTemplates", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    // Unmount before un-stubbing: a hook still mounted would open a real
    // EventSource from a late effect (useSessionWorkspace's subscription).
    cleanup();
    vi.unstubAllGlobals();
  });

  it("lists templates and defaults to default-implementation when the workspace binds none", async () => {
    const { result } = renderHook(() => useRunTemplates(new TemplatesClient(), "session-active"));

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.templates.map((t) => t.id)).toEqual([DEFAULT_TEMPLATE_ID, "tpl_fast"]);
    expect(result.current.defaultTemplateId).toBe(DEFAULT_TEMPLATE_ID);
  });

  it("resolves the workspace's bound template by name to its id", async () => {
    const { result } = renderHook(() =>
      useRunTemplates(new TemplatesClient("fast-path"), "session-active"),
    );

    await waitFor(() => expect(result.current.defaultTemplateId).toBe("tpl_fast"));
  });

  it("accepts a bound template id too", async () => {
    const { result } = renderHook(() =>
      useRunTemplates(new TemplatesClient("tpl_fast"), "session-active"),
    );

    await waitFor(() => expect(result.current.defaultTemplateId).toBe("tpl_fast"));
  });

  it("falls back to default-implementation when the bound template no longer exists", async () => {
    const { result } = renderHook(() =>
      useRunTemplates(new TemplatesClient("deleted-one"), "session-active"),
    );

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.defaultTemplateId).toBe(DEFAULT_TEMPLATE_ID);
  });
});
