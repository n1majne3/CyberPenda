import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ThemeProvider, useTheme, ThemeToggle } from "./ThemeProvider";

// Probe hook to read the current theme inside a provider tree.
function ThemeProbe() {
  const { theme } = useTheme();
  return <div data-testid="theme">{theme}</div>;
}

beforeEach(() => {
  localStorage.clear();
  document.documentElement.className = "";
  // Default to "prefers light" unless a test overrides it.
  definePrefersDark(false);
});

function definePrefersDark(dark: boolean) {
  vi.stubGlobal(
    "matchMedia",
    (query: string) => ({
      matches: dark && query.includes("dark"),
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }),
  );
}

describe("ThemeProvider", () => {
  it("applies no .dark class when system prefers light", () => {
    render(
      <ThemeProvider>
        <ThemeProbe />
      </ThemeProvider>,
    );
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(screen.getByTestId("theme").textContent).toBe("light");
  });

  it("applies .dark when system prefers dark and nothing is stored", () => {
    definePrefersDark(true);
    render(
      <ThemeProvider>
        <ThemeProbe />
      </ThemeProvider>,
    );
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(screen.getByTestId("theme").textContent).toBe("dark");
  });

  it("persists an explicit choice to localStorage", () => {
    definePrefersDark(true); // system dark, but we force light
    render(
      <ThemeProvider>
        <ThemeProbe />
        <ThemeToggle />
      </ThemeProvider>,
    );
    // Toggle from dark → light.
    act(() => {
      screen.getByRole("button").click();
    });
    expect(localStorage.getItem("theme")).toBe("light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("respects a stored preference over system preference", () => {
    localStorage.setItem("theme", "dark");
    definePrefersDark(false); // system light, stored dark wins
    render(
      <ThemeProvider>
        <ThemeProbe />
      </ThemeProvider>,
    );
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("ThemeToggle cycles light → dark → light", async () => {
    definePrefersDark(false);
    const user = userEvent.setup();
    render(
      <ThemeProvider>
        <ThemeToggle />
      </ThemeProvider>,
    );
    const btn = screen.getByRole("button");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    await user.click(btn);
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    await user.click(btn);
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });
});
