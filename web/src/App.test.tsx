import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import App, { ShellErrorBoundary, ShellLayout } from "./App";
import { mockApi } from "@/test/mockApi";
import { ThemeProvider } from "@/components/ThemeProvider";

/** Desktop: sidebar always in a11y tree. Mobile (default setup): closed drawer is inert. */
function mockViewport(mode: "desktop" | "mobile") {
  const matchMedia = (query: string): MediaQueryList => {
    const isMdUp =
      query.includes("min-width: 768px") ||
      query.includes("min-width:768px") ||
      query.includes("(min-width: 768px)");
    return {
      matches: mode === "desktop" ? isMdUp : false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    };
  };
  Object.defineProperty(window, "matchMedia", { value: matchMedia, configurable: true, writable: true });
  Object.defineProperty(globalThis, "matchMedia", {
    value: matchMedia,
    configurable: true,
    writable: true,
  });
}

describe("App", () => {
  beforeEach(() => {
    window.history.pushState({}, "", "/");
    mockViewport("desktop");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    mockViewport("mobile");
  });

  it("shows Skills as a top-level global sidebar page", async () => {
    mockApi({
      "/api/projects": { projects: [] },
    });

    render(
      <StrictMode>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </StrictMode>,
    );

    const skillsLink = await screen.findByRole("link", { name: /skills/i });
    expect(skillsLink).toHaveAttribute("href", "/skills");
  });

  it("exposes skip navigation and visible focus classes for shell navigation", async () => {
    mockApi({
      "/api/projects": { projects: [] },
    });

    render(
      <StrictMode>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </StrictMode>,
    );

    expect(screen.getByRole("link", { name: "Skip to main content" })).toHaveAttribute("href", "#main-content");
    expect(document.querySelector("main")).toHaveAttribute("id", "main-content");
    const newSessionLink = (await screen.findAllByRole("link", { name: /new session/i })).find((link) =>
      link.getAttribute("href") === "/sessions#new-session",
    );
    expect(newSessionLink).toHaveClass("focus-visible:ring-2");
    // Sidebar nav links expose a visible focus ring.
    expect(screen.getByRole("link", { name: /non-project/i })).toHaveClass("focus-visible:ring-2");
  });

  it("renders Geist-styled shell landmarks with active navigation that is not color-only", async () => {
    mockApi({
      "/api/projects": { projects: [] },
    });

    window.history.pushState({}, "", "/skills");

    render(
      <StrictMode>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </StrictMode>,
    );

    expect(await screen.findByRole("complementary", { name: /cyberpenda workspace/i })).toHaveClass(
      "border-sidebar-border",
      "bg-sidebar",
      "text-sidebar-foreground",
    );
    expect(screen.getByRole("navigation", { name: /primary routes/i })).toHaveClass("px-3");
    expect(screen.getByRole("heading", { name: "CyberPenda" })).toHaveClass("text-sm", "font-semibold");

    const skillsLink = screen.getByRole("link", { name: /skills/i });
    expect(skillsLink).toHaveAttribute("aria-current", "page");
    // Active state is distinguished by background + weight + the signal
    // indicator bar — not a per-item outline that reads as a stacked box.
    expect(skillsLink).toHaveClass("bg-sidebar-accent", "font-semibold");
    expect(skillsLink.querySelector('[data-nav-indicator="active"]')).not.toBeNull();

    const projectsLink = screen.getByRole("link", { name: /projects/i });
    expect(projectsLink).not.toHaveAttribute("aria-current");
    expect(projectsLink).toHaveClass("hover:bg-sidebar-accent/70");
  });

  it("keeps global settings directly visible and communicates work disclosure state accessibly", async () => {
    mockApi({
      "/api/projects": { projects: [] },
    });

    render(
      <StrictMode>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </StrictMode>,
    );

    expect(screen.queryByRole("button", { name: /advanced/i })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /runtime profiles/i })).toHaveAttribute("href", "/profiles");
    expect(screen.getByRole("link", { name: /model providers/i })).toHaveAttribute("href", "/model-providers");
    expect(screen.getByRole("link", { name: /credentials/i })).toHaveAttribute("href", "/credentials");
    expect(screen.getByRole("link", { name: /skills/i })).toHaveAttribute("href", "/skills");

    // The Non-project section mirrors the Projects header: a nav link plus an
    // inline "+ New session" action, with no section-level collapse control.
    expect(screen.queryByRole("button", { name: /collapse non-project/i })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /non-project/i })).toHaveAttribute("href", "/sessions");
    expect(screen.getByRole("link", { name: /new session/i })).toHaveAttribute("href", "/sessions#new-session");
  });

  it("applies the shell primitive styling to skip link and theme toggle", async () => {
    mockApi({
      "/api/projects": { projects: [] },
    });

    render(
      <StrictMode>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </StrictMode>,
    );

    expect(screen.getByRole("link", { name: "Skip to main content" })).toHaveClass(
      "focus:bg-background",
      "focus:text-foreground",
      "focus-visible:ring-ring",
    );
    expect(screen.getByRole("button", { name: /switch to/i })).toHaveClass(
      "border-border",
      "bg-background",
      "shadow-sm",
    );
  });

  it("renders route errors in the same shell surface system", async () => {
    const router = createMemoryRouter([
      {
        element: <ShellLayout />,
        errorElement: <ShellErrorBoundary />,
        children: [
          {
            path: "/broken",
            loader: () => {
              throw new Error("Loader exploded");
            },
            element: <div>Broken route</div>,
          },
        ],
      },
    ], {
      initialEntries: ["/broken"],
    });

    render(
      <ThemeProvider>
        <RouterProvider router={router} />
      </ThemeProvider>,
    );

    expect(await screen.findByRole("alert")).toHaveClass("border-destructive/25", "bg-card", "shadow-sm");
    expect(screen.getByRole("heading", { name: "Something went wrong" })).toHaveClass("text-lg", "font-semibold");
    expect(screen.getByText("Loader exploded")).toHaveClass("text-muted-foreground");
  });

  it("does not permanently reserve a fixed sidebar width that squeezes main at ~390px", async () => {
    mockViewport("mobile");
    mockApi({
      "/api/projects": { projects: [] },
    });

    render(
      <StrictMode>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </StrictMode>,
    );

    // Closed mobile drawer is aria-hidden; still present in the DOM for layout classes.
    const sidebar = document.getElementById("workspace-sidebar");
    expect(sidebar).not.toBeNull();
    // Off-canvas below md so main can use the full 390px viewport; desktop keeps w-72 in flow.
    expect(sidebar).toHaveClass("fixed", "inset-y-0", "left-0", "w-72", "md:static");
    expect(sidebar!.className.split(/\s+/)).toEqual(
      expect.arrayContaining(["-translate-x-full", "md:translate-x-0"]),
    );

    const main = document.querySelector("main");
    expect(main).not.toBeNull();
    expect(main).toHaveClass("min-w-0", "flex-1", "overflow-x-hidden");

    // Mobile entry point for the same primary nav (desktop layout unchanged).
    expect(screen.getByRole("button", { name: /open navigation/i })).toHaveClass("md:hidden");
  });

  it("opens the workspace sidebar as an overlay from the mobile menu control", async () => {
    mockViewport("mobile");
    const user = userEvent.setup();
    mockApi({
      "/api/projects": { projects: [] },
    });

    render(
      <StrictMode>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </StrictMode>,
    );

    const sidebar = document.getElementById("workspace-sidebar");
    expect(sidebar).not.toBeNull();
    expect(sidebar).toHaveClass("-translate-x-full");
    expect(sidebar).not.toHaveClass("translate-x-0");
    expect(screen.queryByRole("complementary", { name: /cyberpenda workspace/i })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /open navigation/i }));

    expect(sidebar).toHaveClass("translate-x-0");
    expect(screen.getByRole("button", { name: /close navigation/i })).toBeInTheDocument();
    expect(screen.getByRole("complementary", { name: /cyberpenda workspace/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /projects/i })).toBeInTheDocument();
  });

  it("makes the closed mobile drawer unavailable to keyboard and assistive tech", async () => {
    mockViewport("mobile");
    mockApi({
      "/api/projects": { projects: [] },
    });

    render(
      <StrictMode>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </StrictMode>,
    );

    const sidebar = document.getElementById("workspace-sidebar");
    expect(sidebar).not.toBeNull();
    expect(sidebar).toHaveAttribute("aria-hidden", "true");
    expect(sidebar).toHaveAttribute("inert");
    expect(screen.queryByRole("link", { name: /projects/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("complementary", { name: /cyberpenda workspace/i })).not.toBeInTheDocument();
  });

  it("keeps the desktop sidebar accessible without opening the mobile control", async () => {
    mockViewport("desktop");
    mockApi({
      "/api/projects": { projects: [] },
    });

    render(
      <StrictMode>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </StrictMode>,
    );

    const sidebar = await screen.findByRole("complementary", { name: /cyberpenda workspace/i });
    expect(sidebar).not.toHaveAttribute("aria-hidden", "true");
    expect(sidebar).not.toHaveAttribute("inert");
    expect(screen.getByRole("link", { name: /projects/i })).toBeInTheDocument();
  });

  it("closes the mobile drawer on Escape and restores focus to the menu control", async () => {
    mockViewport("mobile");
    const user = userEvent.setup();
    mockApi({
      "/api/projects": { projects: [] },
    });

    render(
      <StrictMode>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </StrictMode>,
    );

    const openButton = screen.getByRole("button", { name: /open navigation/i });
    await user.click(openButton);

    const sidebar = await screen.findByRole("complementary", { name: /cyberpenda workspace/i });
    expect(sidebar).toHaveClass("translate-x-0");
    expect(screen.getByRole("link", { name: /projects/i })).toBeInTheDocument();

    await user.keyboard("{Escape}");

    await waitFor(() => {
      expect(screen.queryByRole("complementary", { name: /cyberpenda workspace/i })).not.toBeInTheDocument();
    });
    expect(document.getElementById("workspace-sidebar")).toHaveAttribute("aria-hidden", "true");
    expect(openButton).toHaveFocus();
  });

  it("closes the mobile drawer via the dismiss scrim", async () => {
    mockViewport("mobile");
    const user = userEvent.setup();
    mockApi({
      "/api/projects": { projects: [] },
    });

    render(
      <StrictMode>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </StrictMode>,
    );

    const openButton = screen.getByRole("button", { name: /open navigation/i });
    await user.click(openButton);
    expect(await screen.findByRole("complementary", { name: /cyberpenda workspace/i })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /dismiss navigation/i }));

    await waitFor(() => {
      expect(screen.queryByRole("complementary", { name: /cyberpenda workspace/i })).not.toBeInTheDocument();
    });
    expect(openButton).toHaveFocus();
  });
});
