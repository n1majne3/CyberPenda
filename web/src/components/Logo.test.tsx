import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { Logo } from "./Logo";

// CyberPenda logo: user-provided transparent PNG in public/.
describe("Logo", () => {
  it("renders the CyberPenda image", () => {
    const { getByRole } = render(<Logo />);
    const img = getByRole("img", { name: /cyberpenda/i });
    expect(img).toHaveAttribute("src", "/cyberpenda-logo.png");
    expect(img).toHaveAttribute("width", "96");
    expect(img).toHaveAttribute("height", "96");
    expect(img).toHaveAttribute("fetchpriority", "high");
  });

  it("respects a custom size", () => {
    const { getByRole } = render(<Logo className="h-8 w-8" />);
    expect(getByRole("img", { name: /cyberpenda/i })).toHaveClass("h-8", "w-8");
  });

  it("renders without crashing with a bordered variant", () => {
    const { container, getByRole } = render(<Logo bordered />);
    expect(container.firstChild).not.toBeNull();
    expect(getByRole("img", { name: /cyberpenda/i })).toBeInTheDocument();
  });
});
