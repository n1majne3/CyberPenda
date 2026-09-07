import { expect, test } from "@playwright/test";

test("Session Blackboard opens, keeps the composer, and survives reload", async ({ page }, testInfo) => {
  const session = { id: "session-board", title: "Service review", lifecycle: "open", blackboard_protocol: "fgs", run_controls: { blackboard_mode: "working_graph" } };
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const responses: Record<string, unknown> = {
      "/api/sessions": { sessions: [session] },
      "/api/sessions/session-board": session,
      "/api/sessions/session-board/transcript": { entries: [], cursor: 0 },
      "/api/sessions/session-board/timeline": { items: [], cursor: 0 },
      "/api/workspace/navigation": { revision: "1", changed: true, projects: [] },
      "/api/v2/sessions/session-board/fgs": { revision: 1, nodes: [
        { key: "goal:access", type: "goal", title: "Check access", version: 1, state: "active" },
        { key: "step:read", type: "step", action: "Read health response", version: 1, state: "done" },
        { key: "fact:ok", type: "fact", summary: "Service responds", version: 1 },
      ], edges: [{ from: "step:read", to: "goal:access", relation: "toward" }, { from: "step:read", to: "fact:ok", relation: "produces" }] },
      "/api/v2/sessions/session-board/fgs/status": { action_required: 0, receipts: [] },
    };
    await route.fulfill({ json: responses[path] ?? {} });
  });
  await page.goto("/sessions/session-board");
  await page.getByRole("button", { name: "Blackboard", exact: true }).click();
  await expect(page.getByRole("region", { name: "FGS Blackboard" })).toBeVisible();
  await expect(page.getByText("Service responds", { exact: true })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Session message" })).toBeVisible();
  await expect(page).toHaveURL(/view=blackboard/);
  await page.screenshot({ path: testInfo.outputPath("session-blackboard.png"), fullPage: true });
  await page.reload();
  await expect(page.getByRole("button", { name: "Blackboard", exact: true })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText("Service responds", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Conversation", exact: true }).click();
  await expect(page.getByRole("region", { name: "FGS Blackboard" })).toHaveCount(0);
});

for (const width of [1044, 390]) {
  test(`FGS node text stays inside cards at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 850 });
    const action = "Query the current attempt via GET /skill/agent/arena/current/; if empty, claim the next challenge and read the complete problem before starting work.";
    const summary = "Result at https://example.test/" + "long-segment".repeat(30);
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      await route.fulfill({ json: path.endsWith("/fgs") ? {
        revision: 2,
        nodes: [
          { key: "goal:review", type: "goal", version: 1, title: "Review service", state: "open" },
          { key: "step:read", type: "step", version: 1, action, state: "running" },
          { key: "fact:" + "long-key".repeat(30), type: "fact", version: 1, summary },
        ], edges: [],
      } : { sessions: [], projects: [], action_required: 0, receipts: [] } });
    });
    await page.goto("/sessions/layout-check/blackboard");
    const board = page.getByRole("region", { name: "FGS Blackboard" });
    for (const text of [action, summary]) {
      const card = board.getByRole("button", { name: text, exact: false });
      await expect(card).toBeVisible();
      const fits = await card.evaluate((element) => {
        const cardRect = element.getBoundingClientRect();
        return [...element.querySelectorAll("span")].every((span) => {
          const rect = span.getBoundingClientRect();
          return rect.top >= cardRect.top && rect.bottom <= cardRect.bottom &&
            rect.left >= cardRect.left && rect.right <= cardRect.right;
        });
      });
      expect(fits).toBe(true);
    }
    const fact = board.getByRole("button", { name: summary, exact: false });
    await fact.click();
    await expect(board.getByRole("article").getByRole("heading", { name: summary })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`fgs-layout-${width}.png`), fullPage: true });
  });
}
