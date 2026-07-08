import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import {
  BackLink,
  EmptyState,
  PageContainer,
  SettingsAlert,
  SettingsListPanel,
  SettingsPageHeader,
  SettingsPanel,
  SettingsSplitLayout,
} from "./shared";

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

describe("settings helpers", () => {
  it("renders compact settings copy without card nesting", () => {
    const { getByRole, getByText } = render(
      <SettingsPageHeader
        title="Model providers"
        description="Reusable endpoints."
        actions={<button type="button">Add</button>}
      />,
    );

    expect(getByRole("heading", { name: "Model providers" })).toHaveClass(
      "text-lg",
      "tracking-tight",
    );
    expect(getByText("Reusable endpoints.")).toHaveClass("max-w-3xl", "text-muted-foreground");
    expect(getByRole("button", { name: "Add" })).toBeInTheDocument();
  });

  it("uses Geist split layouts and neutral settings panels", () => {
    const { getByTestId } = render(
      <SettingsSplitLayout data-testid="layout">
        <SettingsListPanel data-testid="list">profiles</SettingsListPanel>
        <SettingsPanel data-testid="detail">details</SettingsPanel>
      </SettingsSplitLayout>,
    );

    expect(getByTestId("layout")).toHaveClass(
      "grid",
      "min-w-0",
      "lg:grid-cols-[minmax(220px,280px)_minmax(0,1fr)]",
    );
    expect(getByTestId("list")).toHaveClass("rounded-lg", "border", "bg-card", "p-3");
    expect(getByTestId("detail")).toHaveClass("rounded-lg", "border", "bg-card", "p-4");
  });

  it("uses concise alert copy on a destructive token surface", () => {
    const { getByRole } = render(<SettingsAlert>Save failed.</SettingsAlert>);

    const alert = getByRole("alert");
    expect(alert).toHaveTextContent("Save failed.");
    expect(alert).toHaveClass("rounded-lg", "border-destructive/20", "bg-destructive/5");
  });
});
