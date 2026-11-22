import { fireEvent, screen, within } from "@testing-library/react";

export function openRecentSessions() {
  const region = screen.getByRole("region", { name: /recent sessions/i });
  if (!region.hasAttribute("open")) fireEvent.click(within(region).getByText(/^Recent sessions/));
  return region;
}

export async function findRecentSessions() {
  await screen.findByRole("region", { name: /recent sessions/i });
  return openRecentSessions();
}

export function setDocumentVisibility(value: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { value, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

export const project = (id: string, name: string, updatedAt: string) => ({
  id,
  name,
  description: "",
  scope: {},
  defaults: {},
  created_at: "2026-07-01T00:00:00Z",
  updated_at: updatedAt,
});

export function navigationSummary(id: string, name: string, updatedAt: string, tasks: object[] = [], lastActivityAt?: string) {
  return {
    ...project(id, name, updatedAt),
    last_activity_at: lastActivityAt ?? updatedAt,
    tasks,
  };
}

export const session = (id: string, title: string, lastActivityAt: string) => ({
  id,
  title,
  lifecycle: "open",
  created_at: lastActivityAt,
  updated_at: lastActivityAt,
  last_activity_at: lastActivityAt,
});

export const task = (id: string, goal: string, updatedAt: string, runtimeActivity?: object) => ({
  id,
  project_id: "project-active",
  goal,
  status: "completed",
  runner: "sandbox",
  runtime_profile_id: "profile-1",
  run_controls: {},
  scope_snapshot: {},
  created_at: updatedAt,
  updated_at: updatedAt,
  ...(runtimeActivity ? { runtime_activity: runtimeActivity } : {}),
});

export function response(body: unknown) {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
}
