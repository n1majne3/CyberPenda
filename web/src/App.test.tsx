import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { beforeEach, describe, expect, it } from "vitest";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import App, { ShellErrorBoundary, ShellLayout } from "./App";
import { mockApi } from "@/test/mockApi";
import { ThemeProvider } from "@/components/ThemeProvider";

describe("App", () => {
  beforeEach(() => {
    window.history.pushState({}, "", "/");
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
});
