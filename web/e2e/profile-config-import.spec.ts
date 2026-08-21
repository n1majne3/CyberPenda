import { expect, test, type Page, type Route } from "@playwright/test";

// Profile Config Import flow for issue #226: the config editor opens on the
// provider-native projected file, an import maps structured keys back into
// fields, the remainder lands on the Custom Config File, and the merged
// preview shows the final result.

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

function mergedPreview() {
  return {
    provider: "claude_code",
    merged: {
      env: {
        ANTHROPIC_BASE_URL: "https://api.anthropic.com",
        ANTHROPIC_MODEL: "claude-opus-4-6",
        ANTHROPIC_API_KEY: "REDACTED",
        OVERLAY_FLAG: "adds",
      },
      enabledPlugins: { "warp@claude-code-warp": true },
    },
  };
}

async function routeProfileConfigImport(page: Page) {
  const requests: string[] = [];
  const importBodies: string[] = [];
  const projectedSeed = projectedText();
  // The profiles list is stateful: after the import lands, the stored profile
  // carries the overlay and a fresh updated_at, which is what re-triggers the
  // merged preview read in the app.
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
    } else if (path === `/api/runtime-profiles/${claudeProfile.id}/merged-config-preview` && requests.filter((p) => p === path).length > 1) {
      // Second and later reads happen after the import refreshed the profile.
      body = JSON.stringify(mergedPreview());
    } else if (path === `/api/runtime-profiles/${claudeProfile.id}/merged-config-preview`) {
      body = JSON.stringify({ provider: "claude_code", merged: {} });
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

test("Profile Config Import maps env and keeps the remainder on the Custom Config File", async ({ page }) => {
  const { requests, importBodies } = await routeProfileConfigImport(page);

  await page.goto("/profiles");

  // The profile is selected; its merged preview exists but holds no overlay yet.
  await expect(page.getByRole("button", { name: "Claude Warp" }).first()).toBeVisible();
  await expect(page.getByTestId("merged-config-preview")).not.toContainText("warp@claude-code-warp");

  // The editor opens on the provider-native projected file, never a preview envelope.
  await page.getByRole("button", { name: "Edit config" }).click();
  const editor = page.getByLabel("Runtime config editor");
  await expect(editor).toHaveValue(/ANTHROPIC_MODEL/);
  await expect(editor).not.toHaveValue(/launch_preview/);

  // Add an overlay-only plugin key and a new env var, then import.
  const draft = JSON.stringify(
    {
      env: { ANTHROPIC_MODEL: "claude-opus-4-6", OVERLAY_FLAG: "adds" },
      enabledPlugins: { "warp@claude-code-warp": true },
    },
    null,
    2,
  );
  await editor.fill(draft);
  await page.getByRole("button", { name: "Import config" }).click();

  // The import request carried the edited text verbatim.
  await expect(page.getByTestId("merged-config-preview")).toContainText("warp@claude-code-warp");
  // Editor closed after a successful import.
  await expect(page.getByLabel("Runtime config editor")).toHaveCount(0);
  // The remainder is shown as the Custom Config File.
  await expect(page.getByText("Custom config file (remainder, deep-merged on projection)")).toBeVisible();

  expect(requests).toContain(`/api/runtime-profiles/${claudeProfile.id}/projected-config`);
  expect(requests).toContain(`/api/runtime-profiles/${claudeProfile.id}/import-config`);
  expect(requests).toContain(`/api/runtime-profiles/${claudeProfile.id}/merged-config-preview`);

  // The import carries only the edited text; the daemon derives the
  // Managed Config Key baseline itself.
  const importPayload = JSON.parse(importBodies[0] ?? "{}");
  expect(importPayload.config_text).toContain("OVERLAY_FLAG");
  expect(importPayload.projected_text).toBeUndefined();
});

test("re-opening the config editor shows the Custom Config File remainder", async ({ page }) => {
  const { requests } = await routeProfileConfigImport(page);

  await page.goto("/profiles");

  // Import once so the profile carries a Custom Config File remainder.
  await page.getByRole("button", { name: "Edit config" }).click();
  const editor = page.getByLabel("Runtime config editor");
  await editor.fill(
    JSON.stringify(
      {
        env: { ANTHROPIC_MODEL: "claude-opus-4-6", OVERLAY_FLAG: "adds" },
        enabledPlugins: { "warp@claude-code-warp": true },
      },
      null,
      2,
    ),
  );
  await page.getByRole("button", { name: "Import config" }).click();
  await expect(page.getByLabel("Runtime config editor")).toHaveCount(0);

  // Re-open the editor: the seed now includes the stored remainder, so
  // nothing the operator wrote silently disappears.
  await page.getByRole("button", { name: "Edit config" }).click();
  await expect(page.getByLabel("Runtime config editor")).toHaveValue(/warp@claude-code-warp/);
  await expect(page.getByLabel("Runtime config editor")).toHaveValue(/ANTHROPIC_MODEL/);

  expect(requests).toContain(`/api/runtime-profiles/${claudeProfile.id}/import-config`);
});
