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

// Geist primitive contracts. These assert the visual classes that distinguish
// the shared foundation (neutral surfaces, tight radii, restrained shadows,
// and Vercel-style focus rings) so a future refactor cannot silently regress
// them.

describe("Card", () => {
  it("uses a neutral bordered surface with restrained shadow", () => {
    const { container } = render(<Card>x</Card>);
    expect(container.firstChild).toHaveClass("border", "bg-card", "shadow-sm");
    expect(container.firstChild).not.toHaveClass("ring-1");
  });

  it("renders with an 8px-or-less radius", () => {
    const { container } = render(<Card>x</Card>);
    expect(container.firstChild).toHaveClass("rounded-lg", "bg-card");
    expect(container.firstChild).not.toHaveClass("rounded-xl");
  });

  it("provides default horizontal padding for direct card children", () => {
    const { container } = render(<Card>x</Card>);
    expect(container.firstChild).toHaveClass("p-4");
  });

  it("lets row cards override the default column direction", () => {
    const { container } = render(<Card className="flex-row items-center">x</Card>);
    expect(container.firstChild).toHaveClass("flex-row", "items-center");
    expect(container.firstChild).not.toHaveClass("flex-col");
  });

  it("exposes Geist-compatible variants and sizes", () => {
    const { container } = render(
      <Card variant="flat" size="compact">
        x
      </Card>,
    );
    expect(container.firstChild).toHaveClass("shadow-none", "p-3");
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
  it("defaults to a 4px-rhythm height", () => {
    const { getByRole } = render(<Button>x</Button>);
    expect(getByRole("button")).toHaveClass("h-8");
  });

  it("uses a tight control radius", () => {
    const { getByRole } = render(<Button>x</Button>);
    expect(getByRole("button")).toHaveClass("rounded-md");
    expect(getByRole("button")).not.toHaveClass("rounded-lg");
  });

  it("uses a Vercel-style focus ring without press scaling", () => {
    const { getByRole } = render(<Button>x</Button>);
    expect(getByRole("button")).toHaveClass("focus-visible:ring-2");
    expect(getByRole("button")).not.toHaveClass("focus-visible:ring-3");
    expect(getByRole("button")).not.toHaveClass("active:scale-[0.98]");
  });

  it("destructive variant is solid with readable foreground", () => {
    const { getByRole } = render(<Button variant="destructive">x</Button>);
    const btn = getByRole("button");
    expect(btn).toHaveClass("bg-destructive", "text-destructive-foreground");
    expect(btn.className).not.toMatch(/bg-destructive\/10/);
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
  it("uses a tight radius instead of a pill", () => {
    const { container } = render(<Badge>x</Badge>);
    expect(container.firstChild).toHaveClass("rounded-md");
    expect(container.firstChild).not.toHaveClass("rounded-4xl");
  });

  it("supports compact sizing", () => {
    const { container } = render(<Badge size="sm">x</Badge>);
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

  it("does not expose the removed brand variant", () => {
    const { container } = render(<Badge variant="primary">x</Badge>);
    expect(container.firstChild?.className).not.toMatch(/brand/);
  });

  it("destructive badge is soft", () => {
    const { container } = render(<Badge variant="destructive">x</Badge>);
    expect(container.firstChild!.className).toMatch(/bg-destructive\/10/);
  });
});

describe("Input", () => {
  it("is h-8 with a tight radius", () => {
    const { getByRole } = render(<Input />);
    expect(getByRole("textbox")).toHaveClass("h-8", "rounded-md");
  });

  it("has a Vercel-style focus ring", () => {
    const { getByRole } = render(<Input />);
    expect(getByRole("textbox")).toHaveClass("focus-visible:ring-2");
    expect(getByRole("textbox")).not.toHaveClass("focus-visible:ring-3");
  });

  it("uses a neutral background surface instead of transparent dark overlays", () => {
    const { getByRole } = render(<Input />);
    const input = getByRole("textbox");
    expect(input).toHaveClass("bg-background");
    expect(input.className).not.toMatch(/dark:bg-input\/30/);
  });

  it("exposes sizes and validation variants", () => {
    const { getByRole } = render(<Input size="sm" variant="invalid" />);
    const input = getByRole("textbox");
    expect(input).toHaveClass("h-7", "border-destructive");
  });
});

describe("Textarea", () => {
  it("has a Vercel-style focus ring", () => {
    const { container } = render(<Textarea />);
    expect(container.firstChild).toHaveClass("focus-visible:ring-2");
  });

  it("exposes sizes and validation variants", () => {
    const { container } = render(<Textarea size="lg" variant="invalid" />);
    expect(container.firstChild).toHaveClass("min-h-[120px]", "border-destructive");
  });
});

describe("Label", () => {
  it("renders a label", () => {
    const { container } = render(<Label>field</Label>);
    expect(container.firstChild?.nodeName).toBe("LABEL");
  });

  it("exposes sizes and muted variant", () => {
    const { container } = render(
      <Label size="sm" variant="muted">
        field
      </Label>,
    );
    expect(container.firstChild).toHaveClass("text-xs", "text-muted-foreground");
  });
});

describe("Select", () => {
  it("renders a native select with 4px-rhythm Geist styling", () => {
    const { container } = render(
      <Select>
        <option>a</option>
      </Select>,
    );
    expect(container.firstChild?.nodeName).toBe("SELECT");
    expect(container.firstChild).toHaveClass("h-8", "rounded-md");
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

  it("exposes sizes and validation variants", () => {
    const { container } = render(
      <Select size="sm" variant="invalid">
        <option>a</option>
      </Select>,
    );
    expect(container.firstChild).toHaveClass("h-7", "border-destructive");
  });
});
