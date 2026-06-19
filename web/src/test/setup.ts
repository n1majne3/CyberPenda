import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// jsdom 29 under Vitest 4 does not expose localStorage/matchMedia on the
// global by default. Provide minimal shims so theme and storage tests work.
const store = new Map<string, string>();

if (typeof globalThis.localStorage === "undefined") {
  const shim: Storage = {
    get length() {
      return store.size;
    },
    clear: () => store.clear(),
    getItem: (key: string) => (store.has(key) ? store.get(key)! : null),
    key: (index: number) => Array.from(store.keys())[index] ?? null,
    removeItem: (key: string) => {
      store.delete(key);
    },
    setItem: (key: string, value: string) => {
      store.set(key, String(value));
    },
  };
  Object.defineProperty(globalThis, "localStorage", { value: shim, configurable: true, writable: true });
  Object.defineProperty(window, "localStorage", { value: shim, configurable: true, writable: true });
}

if (typeof globalThis.matchMedia === "undefined") {
  const matchMedia = (query: string): MediaQueryList => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  });
  Object.defineProperty(globalThis, "matchMedia", { value: matchMedia, configurable: true, writable: true });
  Object.defineProperty(window, "matchMedia", { value: matchMedia, configurable: true, writable: true });
}

// Unmount React trees between tests so the DOM does not leak state.
afterEach(() => {
  cleanup();
});
