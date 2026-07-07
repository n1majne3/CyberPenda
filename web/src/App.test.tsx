import { render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { describe, expect, it } from "vitest";
import App from "./App";
import { mockApi } from "@/test/mockApi";
import { ThemeProvider } from "@/components/ThemeProvider";

describe("App", () => {
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
    expect(await screen.findByRole("link", { name: /projects/i })).toHaveClass("focus-visible:ring-3");
    expect(screen.getByRole("button", { name: /advanced/i })).toHaveClass("focus-visible:ring-3");
  });
});
