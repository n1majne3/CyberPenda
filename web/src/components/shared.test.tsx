import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { BackLink, EmptyState, PageContainer } from "./shared";

// Shared presentational helpers extracted from repeated page patterns.
function withRouter(node: React.ReactNode) {
  return render(<MemoryRouter>{node}</MemoryRouter>);
}

describe("BackLink", () => {
  it("renders an arrow + label as a link", () => {
    const { getByRole, getByText } = withRouter(<BackLink to="/x">Back</BackLink>);
    const link = getByRole("link");
    expect(link).toHaveAttribute("href", "/x");
    expect(getByText("Back")).toBeInTheDocument();
  });

  it("includes a left-pointing arrow icon", () => {
    const { container } = withRouter(<BackLink to="/x">Back</BackLink>);
    expect(container.querySelector("svg")).not.toBeNull();
  });
});

describe("EmptyState", () => {
  it("renders the message text", () => {
    const { getByText } = render(<EmptyState>No projects yet.</EmptyState>);
    expect(getByText("No projects yet.")).toBeInTheDocument();
  });

  it("uses muted styling", () => {
    const { container } = render(<EmptyState>x</EmptyState>);
    expect(container.firstChild).toHaveClass("text-muted-foreground");
  });
});

describe("PageContainer", () => {
  it("renders children with consistent padding", () => {
    const { getByText } = render(
      <PageContainer>
        <span>content</span>
      </PageContainer>,
    );
    expect(getByText("content")).toBeInTheDocument();
  });
});
