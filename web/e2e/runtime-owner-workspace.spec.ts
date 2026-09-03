import { expect, test, type Page, type Route } from "@playwright/test";

const projectCount = 123;
const recentTaskCount = 5;

function task(projectIndex: number, taskIndex: number) {
  return {
    id: taskIndex === 0 ? `task-${projectIndex}` : `task-${projectIndex}-${taskIndex}`,
    project_id: `project-${projectIndex}`,
    type: "pentest",
    goal: `Task ${projectIndex}.${taskIndex}`,
    status: "completed",
    runner: "sandbox",
    runtime_profile_id: "profile-1",
    run_controls: {},
    scope_snapshot: {},
    runtime_activity: { liveness: "offline" },
    created_at: "2026-08-01T00:00:00Z",
    updated_at: `2026-08-01T00:00:0${taskIndex}Z`,
  };
}

function navigationProjects() {
  return Array.from({ length: projectCount }, (_, projectIndex) => ({
    id: `project-${projectIndex}`,
    name: `Project ${projectIndex}`,
    description: "",
    kind: "pentest",
    scope: {},
    defaults: {},
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    last_activity_at: "2026-08-01T00:00:05Z",
    tasks: Array.from({ length: recentTaskCount }, (_, taskIndex) => task(projectIndex, taskIndex)),
  }));
}

function transcriptEntries(count: number) {
  return Array.from({ length: count }, (_, index) => ({
    id: `entry-${index + 1}`,
    seq: index + 1,
    continuation: 1,
    kind: "message",
    role: index % 2 === 0 ? "assistant" : "user",
    text: `History entry ${index + 1}`,
    created_at: "2026-08-01T00:00:00Z",
  }));
}

type TranscriptEntry = ReturnType<typeof transcriptEntries>[number];

async function routeRuntimeOwnerWorkspace(page: Page, options: {
  taskStatus?: "completed" | "running";
  transcript?: TranscriptEntry[];
} = {}) {
  const projects = navigationProjects();
  const initialNavigation = JSON.stringify({ revision: "revision-1", changed: true, projects });
  const unchangedNavigation = JSON.stringify({ revision: "revision-1", changed: false, projects: [] });
  const initialTranscript = options.transcript ?? transcriptEntries(200);
  const transcriptHasOlder = options.transcript === undefined;
  const initialTranscriptBody = JSON.stringify({
    task_id: "task-0",
    entries: initialTranscript,
    cursor: initialTranscript.at(-1)?.seq ?? 0,
    has_older: transcriptHasOlder,
  });
  const liveTranscript: TranscriptEntry[] = [];
  const requests: string[] = [];
  const navigationResponses: number[] = [];
  let navigationReads = 0;

  await page.route("**/api/**", async (route: Route) => {
    const requestURL = new URL(route.request().url());
    requests.push(`${requestURL.pathname}${requestURL.search}`);
    let body: string;
    if (requestURL.pathname === "/api/workspace/navigation") {
      body = navigationReads++ === 0 ? initialNavigation : unchangedNavigation;
      navigationResponses.push(new TextEncoder().encode(body).byteLength);
    } else if (requestURL.pathname === "/api/sessions") {
      body = JSON.stringify({ sessions: [] });
    } else if (requestURL.pathname === "/api/projects/project-0/tasks/task-0") {
      const running = options.taskStatus === "running";
      body = JSON.stringify({
        ...task(0, 0),
        status: running ? "running" : "completed",
        goal: "Bounded Runtime Owner history",
        runtime_activity: running ? { liveness: "live", turn_activity: "busy" } : { liveness: "offline" },
        runtime_controls: {
          native_resume_available: true,
          resume_available: true,
          queue_steer_available: true,
          finish_available: false,
          runtime_provider: "codex",
        },
        latest_continuation: {
          id: "continuation-1",
          task_id: "task-0",
          number: 1,
          runtime_profile_id: "profile-1",
          runtime_provider: "codex",
          runner: "sandbox",
          status: running ? "running" : "completed",
          started_at: "2026-08-01T00:00:00Z",
          updated_at: "2026-08-01T00:00:05Z",
          ...(running ? {} : { ended_at: "2026-08-01T00:00:05Z" }),
        },
      });
    } else if (requestURL.pathname.endsWith("/timeline")) {
      body = JSON.stringify({ task_id: "task-0", items: [], cursor: 200, has_older: true });
    } else if (requestURL.pathname.endsWith("/transcript")) {
      const after = Number(requestURL.searchParams.get("after") ?? 0);
      if (after > 0) {
        body = JSON.stringify({
          task_id: "task-0",
          entries: liveTranscript.filter((entry) => entry.seq > after),
          cursor: liveTranscript.at(-1)?.seq ?? initialTranscript.at(-1)?.seq ?? 0,
          has_older: transcriptHasOlder,
        });
      } else {
        body = initialTranscriptBody;
      }
    } else if (requestURL.pathname.endsWith("/finish-readiness")) {
      body = JSON.stringify({ ready_to_finish: true, blockers: [] });
    } else if (requestURL.pathname === "/api/runtime-profiles") {
      body = JSON.stringify({ profiles: [] });
    } else if (requestURL.pathname === "/api/model-providers") {
      body = JSON.stringify({ providers: [] });
    } else if (requestURL.pathname === "/api/runtime-plugins") {
      body = JSON.stringify({ plugins: [] });
    } else {
      body = "{}";
    }
    await route.fulfill({ status: 200, contentType: "application/json", body });
  });

  return {
    requests,
    navigationResponses,
    initialNavigationBytes: new TextEncoder().encode(initialNavigation).byteLength,
    transcriptBytes: new TextEncoder().encode(initialTranscriptBody).byteLength,
    appendTranscript(entry: TranscriptEntry) {
      liveTranscript.push(entry);
    },
  };
}

test("123 Projects use one bounded navigation projection and a small unchanged refresh", async ({ page }) => {
  await page.clock.install();
  const evidence = await routeRuntimeOwnerWorkspace(page);

  await page.goto("/projects/project-0/tasks/task-0");
  await expect(page.getByRole("heading", { name: "Bounded Runtime Owner history" })).toBeVisible();
  await expect(page.locator('section[aria-labelledby^="project-name-"]')).toHaveCount(projectCount);
  await expect(page.locator('#project-tasks-project-0 a[href^="/projects/project-0/tasks/"]')).toHaveCount(recentTaskCount);

  expect(evidence.requests.filter((path) => /^\/api\/projects\/project-\d+\/tasks(?:\?|$)/.test(path))).toEqual([]);
  expect(evidence.initialNavigationBytes).toBeLessThan(700_000);

  await page.clock.fastForward(30_500);
  await expect.poll(() => evidence.navigationResponses.length).toBeGreaterThan(1);
  expect(evidence.navigationResponses.at(-1)).toBeLessThan(200);
});

test("long Runtime Owner history keeps DOM rows bounded and older history reachable", async ({ page }) => {
  const evidence = await routeRuntimeOwnerWorkspace(page);

  await page.goto("/projects/project-0/tasks/task-0");
  await expect(page.getByText("History entry 200")).toBeVisible();
  await expect(page.getByTestId("load-older-transcript")).toBeVisible();

  const renderedRows = await page.getByTestId("transcript-row").count();
  expect(renderedRows).toBeGreaterThan(0);
  expect(renderedRows).toBeLessThan(80);
  expect(evidence.transcriptBytes).toBeLessThan(100_000);
});

test("a page-long final message stays at its beginning when a live Transcript entry arrives", async ({ page }) => {
  const longMessage = Array.from({ length: 180 }, (_, index) => `Page-long message line ${index + 1}`).join("\n");
  const finalMessage: TranscriptEntry = {
    id: "entry-1",
    seq: 1,
    continuation: 1,
    kind: "message",
    role: "assistant",
    text: longMessage,
    created_at: "2026-08-01T00:00:00Z",
  };
  const evidence = await routeRuntimeOwnerWorkspace(page, {
    taskStatus: "running",
    transcript: [finalMessage],
  });

  await page.goto("/projects/project-0/tasks/task-0");
  const viewport = page.getByTestId("conversation-workspace");
  const message = page.getByTestId("transcript-message-bubble").filter({ hasText: "Page-long message line 1" });
  await expect(message).toContainText("Page-long message line 180");
  await expect.poll(() => message.evaluate((element) => element.scrollHeight)).toBeGreaterThan(
    await viewport.evaluate((element) => element.clientHeight),
  );

  await page.getByRole("button", { name: "Scroll to latest (auto-follow on)" }).click();
  await expect.poll(() => viewport.evaluate((element) =>
    element.scrollHeight - element.clientHeight - element.scrollTop,
  )).toBeLessThanOrEqual(2);

  await viewport.hover();
  await page.mouse.wheel(0, -80);
  await expect(page.getByRole("button", { name: "Scroll to latest (auto-follow off)" })).toBeVisible();
  const firstReadingPosition = await viewport.evaluate((element) => element.scrollTop);

  evidence.appendTranscript({
    id: "entry-2",
    seq: 2,
    continuation: 1,
    kind: "message",
    role: "user",
    text: "First live Transcript entry",
    created_at: "2026-08-01T00:00:01Z",
  });
  await expect(page.getByTestId("unseen-transcript-indicator")).toContainText("1 new message", { timeout: 3000 });
  expect(await viewport.evaluate((element) => element.scrollTop)).toBeLessThanOrEqual(firstReadingPosition + 2);

  for (let step = 0; step < 20; step += 1) {
    const atMessageStart = await Promise.all([
      viewport.boundingBox(),
      message.boundingBox(),
    ]).then(([viewportBox, messageBox]) =>
      viewportBox !== null && messageBox !== null && messageBox.y >= viewportBox.y - 2,
    );
    if (atMessageStart) break;
    await page.mouse.wheel(0, -400);
  }

  const readingPosition = await viewport.evaluate((element) => element.scrollTop);
  const [viewportBox, messageBox] = await Promise.all([viewport.boundingBox(), message.boundingBox()]);
  expect(viewportBox).not.toBeNull();
  expect(messageBox).not.toBeNull();
  expect(Math.abs(messageBox!.y - viewportBox!.y)).toBeLessThan(40);

  evidence.appendTranscript({
    id: "entry-3",
    seq: 3,
    continuation: 1,
    kind: "message",
    role: "user",
    text: "Second live Transcript entry",
    created_at: "2026-08-01T00:00:02Z",
  });
  await expect(page.getByTestId("unseen-transcript-indicator")).toContainText("2 new messages", { timeout: 3000 });
  expect(await viewport.evaluate((element) => element.scrollTop)).toBeLessThanOrEqual(readingPosition + 2);
});

test("leaving the live tail keeps the viewport covered without a blank band", async ({ page }) => {
  // Collapsed single-line rows sit far below the 72px virtual-window
  // estimate, so the estimate-derived window used to end above the viewport
  // bottom and expose the tail spacer as a large blank band.
  const shortEntries: TranscriptEntry[] = Array.from({ length: 200 }, (_, index) => ({
    id: `entry-${index + 1}`,
    seq: index + 1,
    continuation: 1,
    kind: "message",
    role: "assistant",
    text: `History entry ${index + 1}`,
    created_at: "2026-08-01T00:00:00Z",
  }));
  await routeRuntimeOwnerWorkspace(page, { transcript: shortEntries });

  await page.goto("/projects/project-0/tasks/task-0");
  await expect(page.getByText("History entry 200")).toBeVisible();

  const viewport = page.getByTestId("conversation-workspace");
  await viewport.hover();
  await page.mouse.wheel(0, -600);
  await expect(page.getByRole("button", { name: "Scroll to latest (auto-follow off)" })).toBeVisible();

  await expect.poll(async () => {
    const [viewportBox, rowsBox] = await Promise.all([
      viewport.boundingBox(),
      page.getByTestId("transcript-rows").boundingBox(),
    ]);
    if (viewportBox === null || rowsBox === null) return false;
    // The rows cannot cover the container's bottom padding, so compare
    // against the content-box bottom.
    const paddingBottom = await viewport.evaluate(
      (element) => parseFloat(getComputedStyle(element).paddingBottom) || 0,
    );
    return rowsBox.y + rowsBox.height >= viewportBox.y + viewportBox.height - paddingBottom - 4;
  }).toBe(true);

  // The coverage extension must stay bounded (#202).
  expect(await page.getByTestId("transcript-row").count()).toBeLessThan(120);
});

test("scrolling up through mixed row heights never exceeds the React update depth", async ({ page }) => {
  // Real transcripts mix collapsed single-line rows with tall messages. This
  // sweep is a browser smoke pass over that mix; the deterministic guard for
  // the covering/over-covering ping-pong (React error #185) lives in
  // virtualWindow.test.ts, where the resonant geometry is exact.
  const mixedEntries: TranscriptEntry[] = Array.from({ length: 200 }, (_, index) => ({
    id: `entry-${index + 1}`,
    seq: index + 1,
    continuation: 1,
    kind: "message",
    role: "assistant",
    text: index === 120
      ? Array.from({ length: 180 }, (_, line) => `Tall message line ${line + 1}`).join("\n")
      : `History entry ${index + 1}`,
    created_at: "2026-08-01T00:00:00Z",
  }));
  await routeRuntimeOwnerWorkspace(page, { transcript: mixedEntries });

  await page.goto("/projects/project-0/tasks/task-0");
  await expect(page.getByText("History entry 200")).toBeVisible();

  const viewport = page.getByTestId("conversation-workspace");
  await viewport.hover();
  await page.mouse.wheel(0, -600);
  await expect(page.getByRole("button", { name: "Scroll to latest (auto-follow off)" })).toBeVisible();
  // The reading position must move smoothly while rows above the viewport are
  // measured: a fixed row may shift at most by the wheel step plus slack, not
  // by the thousands of px a layout mis-anchor used to snap.
  const tallMessage = page.getByText("Tall message line 1");
  let previousY: number | null = null;
  for (let step = 0; step < 24; step += 1) {
    await page.mouse.wheel(0, -300);
    await page.waitForTimeout(120);
    await expect(page.getByRole("heading", { name: "Something went wrong" })).toHaveCount(0);
    if (await tallMessage.count() > 0) {
      const box = await tallMessage.boundingBox();
      if (box) {
        if (previousY !== null) {
          expect(Math.abs(box.y - previousY)).toBeLessThanOrEqual(700);
        }
        previousY = box.y;
      }
    }
  }

  await expect.poll(async () => {
    const [viewportBox, rowsBox] = await Promise.all([
      viewport.boundingBox(),
      page.getByTestId("transcript-rows").boundingBox(),
    ]);
    if (viewportBox === null || rowsBox === null) return false;
    const paddingBottom = await viewport.evaluate(
      (element) => parseFloat(getComputedStyle(element).paddingBottom) || 0,
    );
    return rowsBox.y + rowsBox.height >= viewportBox.y + viewportBox.height - paddingBottom - 4;
  }).toBe(true);
  expect(await page.getByTestId("transcript-row").count()).toBeLessThan(120);
});

test("a tall transcript scrolls from the live tail back to its oldest history", async ({ page }) => {
  // Real transcript rows are far taller than the uniform estimate. Scrolling
  // up must keep measuring rows and shifting the window instead of staying
  // pinned on the same tail rows or snapping back.
  const tallEntries: TranscriptEntry[] = Array.from({ length: 200 }, (_, index) => ({
    id: `entry-${index + 1}`,
    seq: index + 1,
    continuation: 1,
    kind: "message",
    role: "assistant",
    text: Array.from({ length: 20 }, (_, line) => `Entry ${index + 1} line ${line + 1}`).join("\n"),
    created_at: "2026-08-01T00:00:00Z",
  }));
  await routeRuntimeOwnerWorkspace(page, { transcript: tallEntries });

  await page.goto("/projects/project-0/tasks/task-0");
  await expect(page.getByText("Entry 200 line 20")).toBeVisible();

  const viewport = page.getByTestId("conversation-workspace");
  await viewport.hover();
  await page.mouse.wheel(0, -600);
  await expect(page.getByRole("button", { name: "Scroll to latest (auto-follow off)" })).toBeVisible();

  let scrollTop = await viewport.evaluate((element) => element.scrollTop);
  for (let step = 0; step < 80 && scrollTop > 1500; step += 1) {
    await page.mouse.wheel(0, -3000);
    await page.waitForTimeout(50);
    scrollTop = await viewport.evaluate((element) => element.scrollTop);
  }
  expect(scrollTop).toBeLessThan(1500);
  await expect(page.getByRole("heading", { name: "Something went wrong" })).toHaveCount(0);

  const [viewportBox, entryBox] = await Promise.all([
    viewport.boundingBox(),
    page.getByText("Entry 1 line 1").boundingBox(),
  ]);
  expect(viewportBox).not.toBeNull();
  expect(entryBox).not.toBeNull();
  // The oldest message intersects the viewport.
  expect(entryBox!.y + entryBox!.height).toBeGreaterThanOrEqual(viewportBox!.y);
  expect(entryBox!.y).toBeLessThanOrEqual(viewportBox!.y + viewportBox!.height);
  expect(await page.getByTestId("transcript-row").count()).toBeLessThan(120);
});
