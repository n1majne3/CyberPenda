import { act, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage, stubTaskDetailApi } from "./TaskDetailPage.testSupport";

describe("Runtime Owner recovery polling", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("recovers a page opened while the daemon is offline", async () => {
    vi.useFakeTimers();
    const { fetchMock } = stubTaskDetailApi();
    const onlineFetch = fetchMock.getMockImplementation()!;
    let online = false;
    fetchMock.mockImplementation((input, init) => {
      if (!online && String(input).endsWith("/api/projects/project-1/tasks/task-1")) {
        return Promise.resolve(new Response(JSON.stringify({ error: "daemon unavailable" }), { status: 503 }));
      }
      return onlineFetch(input, init);
    });
    await act(async () => { renderPage(); });
    expect(screen.getByText("daemon unavailable")).toBeInTheDocument();
    online = true;
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(screen.getByRole("textbox", { name: "Task message" })).toBeInTheDocument();
    expect(screen.queryByText("daemon unavailable")).not.toBeInTheDocument();
  });

  it.each(["pending", "stopped", "failed"])("receives late Steering settlement after %s", async (status) => {
    vi.useFakeTimers();
    const { fetchMock } = stubTaskDetailApi({
      status,
      runtime_controls: { native_steer_state: "pending", runtime_provider: "pi" },
    });
    await act(async () => { renderPage(); });
    expect(screen.getByTestId("steer-pending-badge")).toBeInTheDocument();

    const fetchBeforeSettlement = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation(async (input, init) => {
      const response = await fetchBeforeSettlement(input, init);
      if (!String(input).endsWith("/api/projects/project-1/tasks/task-1")) return response;
      const owner = await response.json();
      return new Response(JSON.stringify({
        ...owner,
        status: "stopped",
        runtime_controls: {
          ...owner.runtime_controls,
          native_steer_state: "action_required",
          native_steer_error: "native steer canceled",
        },
      }), { status: 200, headers: { "Content-Type": "application/json" } });
    });

    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(screen.queryByTestId("steer-pending-badge")).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("native steer canceled");
  });
});
