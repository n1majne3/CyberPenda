import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import {
  BackLink,
  EmptyState,
  ErrorState,
  LoadingState,
  PageContainer,
  RichEmptyState,
  SectionHeading,
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

describe("RichEmptyState", () => {
  it("renders a centered dashed card with icon, title, description, and action", () => {
    const { getByRole, getByText, container } = render(
      <RichEmptyState
        icon={<span data-testid="icon" />}
        title="No sessions"
        description="Create one to start."
        action={<button type="button">New session</button>}
      />,
    );

    const status = getByRole("status");
    expect(status).toHaveClass("border-dashed", "py-14", "text-center");
    expect(getByText("No sessions")).toHaveClass("font-medium");
    expect(getByText("Create one to start.")).toHaveClass("text-muted-foreground");
    expect(getByRole("button", { name: "New session" })).toBeInTheDocument();
    // icon avatar box present
    expect(container.querySelector(".h-10.w-10")).not.toBeNull();
  });

  it("omits description and action when not provided", () => {
    const { getByText, queryByRole } = render(<RichEmptyState title="Empty" />);
    expect(getByText("Empty")).toBeInTheDocument();
    expect(queryByRole("button")).toBeNull();
  });
});

describe("ErrorState", () => {
  it("renders a destructive alert with avatar, title, and message", () => {
    const { getByRole, getByText, container } = render(
      <ErrorState error="network down" title="Couldn't load sessions" />,
    );
    const alert = getByRole("alert");
    expect(alert).toHaveClass("border-destructive/25");
    expect(getByText("Couldn't load sessions")).toHaveClass("font-medium");
    expect(getByText("network down")).toHaveClass("text-muted-foreground");
    // destructive icon avatar
    expect(container.querySelector(".border-destructive\\/20")).not.toBeNull();
  });

  it("renders nothing when error is null", () => {
    const { container } = render(<ErrorState error={null} />);
    expect(container.firstChild).toBeNull();
  });
});

describe("LoadingState", () => {
  it("renders a status with a spinner and label", () => {
    const { getByRole, container } = render(<LoadingState label="Loading sessions" />);
    const status = getByRole("status", { name: "Loading sessions" });
    expect(status).toHaveTextContent("Loading sessions");
    const spinner = container.querySelector(".animate-spin");
    expect(spinner).not.toBeNull();
  });

  it("accepts a custom minHeight", () => {
    const { getByRole } = render(<LoadingState label="x" minHeight="min-h-24" />);
    expect(getByRole("status")).toHaveClass("min-h-24");
  });
});

describe("SectionHeading", () => {
  it("renders a semibold h3 with optional count and muted variant", () => {
    const { getByRole, rerender } = render(<SectionHeading count={3}>Findings</SectionHeading>);
    const h3 = getByRole("heading", { level: 3 });
    expect(h3).toHaveClass("font-semibold", "tracking-tight");
    expect(h3).toHaveTextContent("Findings (3)");

    rerender(<SectionHeading muted>Saved</SectionHeading>);
    expect(getByRole("heading", { level: 3 })).toHaveClass("text-muted-foreground");
  });
});
