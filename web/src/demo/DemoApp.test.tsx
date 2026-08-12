import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DemoApp } from "./DemoApp";

describe("public CyberPenda Demo", () => {
  it("shows a read-only Project and blocks Task Launch", async () => {
    window.history.replaceState({}, "", "/");
    render(<DemoApp />);

    expect(screen.getByText("Public demo · Read only")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Acme External Pentest" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Launch Task" })).toBeDisabled();

    fireEvent.click(screen.getAllByRole("link", { name: "Findings" })[0]);
    expect(await screen.findByRole("heading", { name: "Confirmed Findings" })).toBeInTheDocument();
    expect(screen.getByText("Admin session cookie lacks Secure attribute")).toBeInTheDocument();

    fireEvent.click(screen.getAllByRole("link", { name: "Report" })[0]);
    expect(await screen.findByRole("heading", { name: "Report preview" })).toBeInTheDocument();
    expect(screen.getAllByText(/authorized demonstration data/i)).toHaveLength(2);
  });

  it("opens a Demo section from a direct Vercel URL", () => {
    window.history.replaceState({}, "", "/report");
    render(<DemoApp />);

    expect(screen.getByRole("heading", { name: "Report preview" })).toBeInTheDocument();
  });
});
