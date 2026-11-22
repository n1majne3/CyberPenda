import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { mockApi } from "@/test/mockApi";
import { renderPage } from "./SkillsPage.testSupport";

describe("SkillsPage", () => {

  it("opens Skill instructions read-only and discards edits on Cancel without publishing", async () => {
    const skill = { id: "recon-helper", name: "Recon Helper", description: "Review authorized targets", enabled: true, files: { "SKILL.md": "# Recon Helper\n\nOnly use authorized targets." } };
    const fetchMock = mockApi({
      "/api/runtime-profiles": { profiles: [] },
      "/api/skills/recon-helper": skill,
      "/api/skills": { skills: [skill] },
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "View Recon Helper" }));
    expect(await screen.findByRole("article", { name: "Skill instructions" })).toHaveTextContent("Only use authorized targets.");
    expect(screen.queryByLabelText("Skill ID")).not.toBeInTheDocument();
    await userEvent.click(screen.getByText("More"));
    await userEvent.click(screen.getByRole("button", { name: "Edit Skill" }));
    await userEvent.type(screen.getByLabelText("Name"), " draft");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("heading", { name: "Recon Helper" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Skill ID")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  });
});
