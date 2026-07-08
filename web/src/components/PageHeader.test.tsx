import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { PageHeader, PageHeaderTitle, PageHeaderActions } from "./PageHeader";

// PageHeader is the consistent top bar. It renders a title row plus an
// optional actions slot.
describe("PageHeader", () => {
  it("renders the title", () => {
    const { getByText } = render(
      <PageHeader>
        <PageHeaderTitle>Projects</PageHeaderTitle>
      </PageHeader>,
    );
    expect(getByText("Projects")).toBeInTheDocument();
  });

  it("has the sticky top-bar styling (h-12 border-b)", () => {
    const { container } = render(
      <PageHeader>
        <PageHeaderTitle>x</PageHeaderTitle>
      </PageHeader>,
    );
    expect(container.firstChild).toHaveClass("h-12", "border-b");
  });

  it("exposes Geist-compatible variants and sizes", () => {
    const { container } = render(
      <PageHeader variant="solid" size="compact">
        <PageHeaderTitle size="sm">Projects</PageHeaderTitle>
      </PageHeader>,
    );
    expect(container.firstChild).toHaveClass("h-10", "px-3", "bg-background");
    expect(container.firstChild).not.toHaveClass("h-12", "bg-background/80");
    expect(container.querySelector("h2")).toHaveClass("text-xs");
  });

  it("renders an actions slot on the right", () => {
    const { getByText } = render(
      <PageHeader>
        <PageHeaderTitle>x</PageHeaderTitle>
        <PageHeaderActions>
          <button>Launch</button>
        </PageHeaderActions>
      </PageHeader>,
    );
    expect(getByText("Launch")).toBeInTheDocument();
  });
});
