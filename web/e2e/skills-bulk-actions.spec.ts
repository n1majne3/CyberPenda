import { expect, test } from "@playwright/test";

for (const zoom of [100, 125, 150]) {
  const scale = zoom / 100;
  test.describe(`Skills at ${zoom}% zoom`, () => {
    test.use({
      viewport: { width: Math.round(1280 / scale), height: Math.round(800 / scale) },
      deviceScaleFactor: scale,
    });

    test("Global and Profile Skill bulk actions fit in their cards", async ({ page }) => {
      await page.route("**/api/**", async (route) => {
        const path = new URL(route.request().url()).pathname;
        const json = path === "/api/runtime-profiles"
          ? { profiles: [{ id: "profile-1", name: "Codex Default", provider: "codex", fields: {} }] }
          : path === "/api/skills"
            ? { skills: [{ id: "example", name: "Example Skill", enabled: true }] }
            : {};
        await route.fulfill({ json });
      });

      await page.goto("/skills");
      const global = page.getByRole("group", { name: "Global Skill bulk actions" });
      const profile = page.getByRole("group", { name: "Codex Default Profile Skill bulk actions" });
      await expect(global).toContainText("1/1 enabled");
      await expect(profile).toContainText("1/1 allowed");
      await page.evaluate(() => document.fonts.ready.then(() => undefined));

      const globalBox = (await global.boundingBox())!;
      const profileBox = (await profile.boundingBox())!;
      if (zoom === 125) {
        expect(globalBox.y + globalBox.height).toBeLessThanOrEqual(profileBox.y);
      } else {
        expect(globalBox.x + globalBox.width).toBeLessThanOrEqual(profileBox.x);
      }

      for (const [group, box, description] of [
        [global, globalBox, "Direct launches and every Runtime Profile"],
        [profile, profileBox, "Only this Profile; Global Opt-Outs still override"],
      ] as const) {
        const descriptionLines = await group.getByText(description, { exact: true }).evaluate((element) =>
          element.getBoundingClientRect().height / parseFloat(getComputedStyle(element).lineHeight),
        );
        expect(descriptionLines).toBeLessThanOrEqual(3);

        const buttons = group.getByRole("button", { name: /^(Enable|Disable) all skills/ });
        await expect(buttons).toHaveCount(2);
        for (const button of await buttons.all()) {
          await expect(button).toBeInViewport();
          const buttonBox = (await button.boundingBox())!;
          expect(buttonBox.x).toBeGreaterThanOrEqual(box.x);
          expect(buttonBox.x + buttonBox.width).toBeLessThanOrEqual(box.x + box.width);
        }
      }
    });
  });
}
