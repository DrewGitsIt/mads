import { describe, expect, it, vi } from "vitest";
import { FakeRouterClient } from "../../../router/fixtures";
import {
  COMMANDS,
  availability,
  findCommand,
  matchCommands,
  parseCommandLine,
} from "./registry";
import type { CommandContext, CommandDescriptor } from "./types";

const ctx: CommandContext = {
  routerClient: new FakeRouterClient(),
  sessionId: "s1",
};

const commands: CommandDescriptor[] = [
  {
    name: "usage",
    aliases: ["status", "context"],
    description: "",
    execute: vi.fn(),
  },
  { name: "new", description: "", execute: vi.fn() },
  { name: "new-leader", description: "", execute: vi.fn() },
  { name: "secret", description: "", visible: () => false, execute: vi.fn() },
  {
    name: "compact",
    description: "",
    available: () => ({ ok: false, reason: "not supported by goose" }),
    execute: vi.fn(),
  },
];

describe("command registry", () => {
  it("registers /task and /model, each name unique", () => {
    const names = COMMANDS.map((c) => c.name);
    expect(names).toEqual(expect.arrayContaining(["task", "model"]));
    // No /agent: a persona swap has no live-apply path, so it would only
    // relabel the screen (the dock's persona segment is read-only for the same reason).
    expect(names).not.toContain("agent");
    expect(new Set(names).size).toBe(names.length);
  });

  it("finds a command by name or alias, case-insensitively", () => {
    expect(findCommand("usage", ctx, commands)?.name).toBe("usage");
    expect(findCommand("STATUS", ctx, commands)?.name).toBe("usage");
    expect(findCommand("context", ctx, commands)?.name).toBe("usage");
    expect(findCommand("nope", ctx, commands)).toBeUndefined();
  });

  it("treats an invisible command as unregistered", () => {
    expect(findCommand("secret", ctx, commands)).toBeUndefined();
    expect(matchCommands("sec", ctx, commands)).toEqual([]);
  });

  it("matches names and aliases by substring, putting an exact match first", () => {
    expect(
      matchCommands("new", ctx, commands).map((m) => m.command.name),
    ).toEqual(["new", "new-leader"]);
    expect(
      matchCommands("leader", ctx, commands).map((m) => m.command.name),
    ).toEqual(["new-leader"]);
    expect(matchCommands("stat", ctx, commands)).toEqual([
      { command: commands[0], alias: "status" },
    ]);
  });

  it("reports availability, defaulting to ok", () => {
    expect(availability(commands[0], ctx)).toEqual({ ok: true });
    expect(availability(commands[4], ctx)).toEqual({
      ok: false,
      reason: "not supported by goose",
    });
  });

  it("parses a leading /name and its args", () => {
    expect(
      parseCommandLine("/task reproduce DNS error for tracing service"),
    ).toEqual({
      name: "task",
      args: "reproduce DNS error for tracing service",
    });
    expect(parseCommandLine("  /usage  ")).toEqual({ name: "usage", args: "" });
    expect(parseCommandLine("/export\njson")).toEqual({
      name: "export",
      args: "json",
    });
    expect(parseCommandLine("please /task x")).toBeNull();
    expect(parseCommandLine("/usr/bin/env")).toBeNull();
  });
});
