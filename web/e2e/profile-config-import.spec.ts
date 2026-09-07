import { expect, test, type Page, type Route } from "@playwright/test";

// Browser contract: load saved configuration on demand, import, and reopen.
// Field validation and import errors are covered by component and daemon tests.

const claudeProfile = {
  id: "profile-1",
  name: "Claude Warp",
  provider: "claude_code",
  fields: {
    model: "claude-opus-4-6",
    env: { ANTHROPIC_MODEL: "claude-opus-4-6" },
    custom_config_file: "",
  },
  created_at: "2026-08-21T00:00:00Z",
  updated_at: "2026-08-21T00:00:00Z",
};

const importedProfile = {
  ...claudeProfile,
  updated_at: "2026-08-21T00:01:00Z",
  fields: {
    ...claudeProfile.fields,
    env: { ANTHROPIC_MODEL: "claude-opus-4-6", OVERLAY_FLAG: "adds" },
    custom_config_file: '{\n  "enabledPlugins": {\n    "warp@claude-code-warp": true\n  }\n}\n',
  },
};

function projectedText() {
  return JSON.stringify(
    {
      env: {
        ANTHROPIC_BASE_URL: "https://api.anthropic.com",
        ANTHROPIC_MODEL: "claude-opus-4-6",
        ANTHROPIC_API_KEY: "REDACTED",
      },
    },
    null,
    2,
  );
}

async function routeProfileConfigImport(page: Page) {
  const requests: string[] = [];
  const importBodies: string[] = [];
  const projectedSeed = projectedText();
  // A successful import changes the saved Profile and its configuration.
  let imported = false;
  await page.route("**/api/**", async (route: Route) => {
    const requestURL = new URL(route.request().url());
    const path = requestURL.pathname;
    requests.push(path);
    let body: string;
    if (path === "/api/runtime-profiles") {
      body = JSON.stringify({ profiles: [imported ? importedProfile : claudeProfile] });
    } else if (path === `/api/runtime-profiles/${claudeProfile.id}/projected-config`) {
      // Once imported, the editor seed re-opens on the merged file: the
      // structured projection plus the Custom Config File remainder.
      body = JSON.stringify({
        provider: "claude_code",
        format: "json",
        text: imported
          ? JSON.stringify(
              {
                env: {
                  ANTHROPIC_BASE_URL: "https://api.anthropic.com",
                  ANTHROPIC_MODEL: "claude-opus-4-6",
                  ANTHROPIC_API_KEY: "REDACTED",
                  OVERLAY_FLAG: "adds",
                },
                enabledPlugins: { "warp@claude-code-warp": true },
              },
              null,
              2,
            )
          : projectedSeed,
        custom_config_file: imported ? importedProfile.fields.custom_config_file : "",
      });
    } else if (path === `/api/runtime-profiles/${claudeProfile.id}/import-config`) {
      imported = true;
      importBodies.push(route.request().postData() ?? "");
      body = JSON.stringify({
        profile: importedProfile,
        mapped_keys: ["env"],
      });
    } else if (path === "/api/model-providers") {
      body = JSON.stringify({ providers: [] });
    } else if (path === "/api/runtime-plugins") {
      body = JSON.stringify({ plugins: [] });
    } else if (path === "/api/runtime-extensions") {
      body = JSON.stringify({ extensions: [], items: [] });
    } else if (path === "/api/sessions") {
      body = JSON.stringify({ sessions: [] });
    } else {
      body = "{}";
    }
    await route.fulfill({ status: 200, contentType: "application/json", body });
  });
  return { requests, importBodies };
}

test("Profile Config Import retains custom settings after reopening", async ({ page }) => {
  const { requests, importBodies } = await routeProfileConfigImport(page);
  await page.goto("/profiles");
  await expect(page.getByRole("button", { name: "Claude Warp" }).first()).toBeVisible();
  await expect(page.getByRole("region", { name: "Actual runtime config" })).toHaveCount(0);
  expect(requests).not.toContain(`/api/runtime-profiles/${claudeProfile.id}/projected-config`);

  await page.getByRole("button", { name: "View actual config" }).click();
  const config = page.getByRole("region", { name: "Actual runtime config" });
  await expect(config).toContainText("ANTHROPIC_MODEL");
  await config.getByRole("button", { name: "Edit config" }).click();
  const editor = page.getByLabel("Runtime config editor");
  await expect(editor).toHaveValue(/ANTHROPIC_MODEL/);
  const draft = JSON.stringify({
    env: { ANTHROPIC_MODEL: "claude-opus-4-6", OVERLAY_FLAG: "adds" },
    enabledPlugins: { "warp@claude-code-warp": true },
  }, null, 2);
  await editor.fill(draft);
  await config.getByRole("button", { name: "Import config" }).click();
  await expect(editor).toHaveCount(0);
  expect(importBodies).toHaveLength(1);
  expect(JSON.parse(importBodies[0])).toEqual({ config_text: draft });

  await expect(config).toHaveCount(0);
  await page.getByRole("button", { name: "View actual config" }).click();
  await config.getByRole("button", { name: "Edit config" }).click();
  await expect(editor).toHaveValue(/warp@claude-code-warp/);
  await expect(editor).toHaveValue(/OVERLAY_FLAG/);
  await expect(editor).toHaveValue(/ANTHROPIC_MODEL/);
});
