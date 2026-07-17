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
    expect(await screen.findByRole("link", { name: /projects/i })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("button", { name: /advanced/i })).toHaveClass("focus-visible:ring-2");
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
    expect(screen.getByRole("navigation", { name: /primary routes/i })).toHaveClass("p-3");
    expect(screen.getByRole("heading", { name: "CyberPenda" })).toHaveClass("text-sm", "font-semibold");

    const skillsLink = screen.getByRole("link", { name: /skills/i });
    expect(skillsLink).toHaveAttribute("aria-current", "page");
    expect(skillsLink).toHaveAttribute("data-active", "true");
    expect(skillsLink).toHaveClass("border-sidebar-border", "bg-sidebar-accent", "font-semibold");
    expect(skillsLink.querySelector('[data-nav-indicator="active"]')).not.toBeNull();

    const projectsLink = screen.getByRole("link", { name: /projects/i });
    expect(projectsLink).toHaveAttribute("data-active", "false");
    expect(projectsLink).toHaveClass("hover:border-sidebar-border", "hover:bg-sidebar-accent/70");
  });

  it("communicates the advanced navigation disclosure state accessibly", async () => {
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

    const advancedButton = screen.getByRole("button", { name: /show advanced routes/i });
    expect(advancedButton).toHaveAttribute("aria-expanded", "false");
    expect(advancedButton).toHaveAttribute("data-state", "closed");
    expect(screen.queryByRole("link", { name: /runtime profiles/i })).not.toBeInTheDocument();

    await user.click(advancedButton);

    expect(screen.getByRole("button", { name: /hide advanced routes/i })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("button", { name: /hide advanced routes/i })).toHaveAttribute("data-state", "open");
    expect(screen.getByRole("link", { name: /runtime profiles/i })).toHaveClass("border-transparent");
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
    // Off-canvas below md so main can use the full 390px viewport; desktop keeps w-64 in flow.
    expect(sidebar).toHaveClass("fixed", "inset-y-0", "left-0", "w-64", "md:static");
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
