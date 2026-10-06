import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// Node >= 25 defines global localStorage/sessionStorage accessors that
// return undefined unless --localstorage-file is passed, and — because
// happy-dom's window IS globalThis under vitest — they shadow happy-dom's
// own Storage. Install an in-memory stand-in so tests behave the same on
// Node 22 (CI) and newer.
class MemoryStorage implements Storage {
  private store = new Map<string, string>();
  get length() {
    return this.store.size;
  }
  key(index: number) {
    return [...this.store.keys()][index] ?? null;
  }
  getItem(key: string) {
    return this.store.get(key) ?? null;
  }
  setItem(key: string, value: string) {
    this.store.set(key, String(value));
  }
  removeItem(key: string) {
    this.store.delete(key);
  }
  clear() {
    this.store.clear();
  }
}

for (const name of ["localStorage", "sessionStorage"] as const) {
  if (typeof globalThis[name] === "undefined") {
    Object.defineProperty(globalThis, name, {
      value: new MemoryStorage(),
      configurable: true,
      writable: true,
    });
  }
}

afterEach(() => {
  cleanup();
  // One in-memory store for the whole file: without this, state a test persisted
  // (the rail's seen-session ids, say) decides what the next test renders.
  localStorage.clear();
});
