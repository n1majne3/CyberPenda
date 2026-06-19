import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
  CardFooter,
  Button,
  Badge,
  Input,
  Textarea,
  Label,
  Select,
} from "./ui";

// Multica-specific primitive contracts. These assert the visual classes that
// distinguish the new design system (flat ring elevation, soft destructive,
// pill badges, focus rings) so a future refactor cannot silently regress them.

describe("Card", () => {
  it("uses flat ring elevation (ring, not just border)", () => {
    const { container } = render(<Card>x</Card>);
    expect(container.firstChild).toHaveClass("ring-1");
  });

  it("renders with the xl radius and card surface", () => {
    const { container } = render(<Card>x</Card>);
    expect(container.firstChild).toHaveClass("rounded-xl", "bg-card");
  });

  it("provides default horizontal padding for direct card children", () => {
    const { container } = render(<Card>x</Card>);
    expect(container.firstChild).toHaveClass("px-4");
  });

  it("lets row cards override the default column direction", () => {
    const { container } = render(<Card className="flex-row items-center">x</Card>);
    expect(container.firstChild).toHaveClass("flex-row", "items-center");
    expect(container.firstChild).not.toHaveClass("flex-col");
  });

  it("supports header/title/description/content/footer sub-parts", () => {
    const { getByText } = render(
      <Card>
        <CardHeader>
          <CardTitle>t</CardTitle>
          <CardDescription>d</CardDescription>
        </CardHeader>
        <CardContent>c</CardContent>
        <CardFooter>f</CardFooter>
      </Card>,
    );
    expect(getByText("t")).toBeInTheDocument();
    expect(getByText("d")).toBeInTheDocument();
    expect(getByText("c")).toBeInTheDocument();
    expect(getByText("f")).toBeInTheDocument();
  });
});

describe("Button", () => {
  it("defaults to h-8 height (multica dense sizing)", () => {
    const { getByRole } = render(<Button>x</Button>);
    expect(getByRole("button")).toHaveClass("h-8");
  });

  it("uses the lg radius", () => {
    const { getByRole } = render(<Button>x</Button>);
    expect(getByRole("button")).toHaveClass("rounded-lg");
  });

  it("destructive variant is soft (translucent bg, not solid)", () => {
    const { getByRole } = render(<Button variant="destructive">x</Button>);
    const btn = getByRole("button");
    expect(btn.className).toMatch(/bg-destructive\/10/);
    expect(btn.className).toMatch(/text-destructive/);
  });

  it("supports icon size (square)", () => {
    const { getByRole } = render(<Button size="icon">x</Button>);
    expect(getByRole("button")).toHaveClass("w-8", "h-8");
  });

  it("forwards extra className and merges", () => {
    const { getByRole } = render(<Button className="w-full">x</Button>);
    expect(getByRole("button")).toHaveClass("w-full", "h-8");
  });
});

describe("Badge", () => {
  it("is a pill (rounded-4xl)", () => {
    const { container } = render(<Badge>x</Badge>);
    expect(container.firstChild).toHaveClass("rounded-4xl");
  });

  it("has a fixed compact height", () => {
    const { container } = render(<Badge>x</Badge>);
    expect(container.firstChild).toHaveClass("h-5");
  });

  it.each([
    ["default"],
    ["primary"],
    ["success"],
    ["warning"],
    ["destructive"],
    ["outline"],
  ] as const)("supports the %s variant", (variant) => {
    const { container } = render(<Badge variant={variant}>x</Badge>);
    expect(container.firstChild).not.toBeNull();
  });

  it("destructive badge is soft", () => {
    const { container } = render(<Badge variant="destructive">x</Badge>);
    expect(container.firstChild!.className).toMatch(/bg-destructive\/10/);
  });
});

describe("Input", () => {
  it("is h-8 with lg radius", () => {
    const { getByRole } = render(<Input />);
    expect(getByRole("textbox")).toHaveClass("h-8", "rounded-lg");
  });

  it("has a focus ring (ring-3)", () => {
    const { getByRole } = render(<Input />);
    expect(getByRole("textbox").className).toMatch(/focus-visible:ring-3/);
  });
});

describe("Textarea", () => {
  it("has a focus ring", () => {
    const { container } = render(<Textarea />);
    expect(container.firstChild!.className).toMatch(/focus-visible:ring-3/);
  });
});

describe("Label", () => {
  it("renders a label", () => {
    const { container } = render(<Label>field</Label>);
    expect(container.firstChild?.nodeName).toBe("LABEL");
  });
});

describe("Select", () => {
  it("renders a native select with the dense multica styling", () => {
    const { container } = render(
      <Select>
        <option>a</option>
      </Select>,
    );
    expect(container.firstChild?.nodeName).toBe("SELECT");
    expect(container.firstChild).toHaveClass("h-8", "rounded-lg");
  });

  it("is a controlled-compatible select (forwards value/onChange)", () => {
    const { container } = render(
      <Select value="a" onChange={() => {}}>
        <option value="a">a</option>
        <option value="b">b</option>
      </Select>,
    );
    expect((container.firstChild as HTMLSelectElement).value).toBe("a");
  });
});
