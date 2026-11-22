import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage, stubTaskDetailApi } from "./TaskDetailPage.testSupport";

describe("TaskDetailPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });


  it("keeps a quiet Runtime identity without bordering assistant messages", async () => {
    stubTaskDetailApi();

    renderPage("/projects/project-1/tasks/task-1?view=conversation");

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    const message = screen.getByTestId("transcript-message-bubble");
    expect(message).not.toHaveClass("border-l-2");
    expect(within(screen.getByTestId("transcript-turn-header")).getByText("Codex")).toHaveClass("text-signal");
    expect(message).not.toHaveClass("rounded-lg");
  });


  it("renders safe Claude runtime text as a visible assistant message", async () => {
    stubTaskDetailApi({}, [
      {
        id: "runtime-entry",
        seq: 2,
        continuation: 1,
        kind: "message",
        role: "assistant",
        text: "Inspecting the scoreboard now.",
        created_at: "2026-01-01T00:00:01Z",
      },
    ]);

    renderPage();

    expect(await screen.findByText("Inspecting the scoreboard now.")).toBeInTheDocument();
    const assistantMessage = screen.getByTestId("transcript-message-bubble");
    expect(assistantMessage).toBeInTheDocument();
    expect(assistantMessage.previousElementSibling).toHaveAttribute("data-testid", "transcript-turn-header");
    expect(screen.queryByText(/"type":"assistant"/)).not.toBeInTheDocument();
  });


  it("projects Claude tool calls and results into readable transcript rows", async () => {
    stubTaskDetailApi({}, [
      {
        id: "assistant-message",
        seq: 7,
        continuation: 1,
        kind: "message",
        role: "assistant",
        text: "I will inspect the target now.",
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "assistant-tool-call",
        seq: 7,
        continuation: 1,
        kind: "tool_call",
        role: "assistant",
        tool_call_id: "call-1",
        tool_name: "Bash",
        details: { input: { command: "curl http://localhost:3000" } },
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "tool-result",
        seq: 8,
        continuation: 1,
        kind: "tool_result",
        role: "tool",
        text: "HTTP/1.1 200 OK\nbody",
        tool_call_id: "call-1",
        created_at: "2026-01-01T00:00:02Z",
      },
    ]);

    renderPage();

    expect(await screen.findByText("I will inspect the target now.")).toBeInTheDocument();
    expect(screen.getByText("Bash · curl http://localhost:3000")).toBeInTheDocument();
    expect(screen.getAllByText(/HTTP\/1\.1 200 OK/)).toHaveLength(2);
    expect(screen.queryByText(/"type":"assistant"/)).not.toBeInTheDocument();
    expect(screen.queryByText(/"type":"user"/)).not.toBeInTheDocument();
    const toolRows = screen.getAllByTestId("transcript-tool-row");
    expect(toolRows).toHaveLength(1);
    expect(toolRows[0]).not.toHaveAttribute("open");
    expect(toolRows[0]).not.toHaveClass("rounded-md");
    expect(toolRows[0]).not.toHaveClass("bg-card/60");
    const resultBody = screen.getAllByText(/HTTP\/1\.1 200 OK/).find((element) => element.tagName === "PRE");
    expect(resultBody).toBeDefined();
    expect(resultBody?.textContent).not.toContain("tool_call_id: call-1");
  });


  it("renders completed reasoning as collapsed activity instead of an agent message", async () => {
    stubTaskDetailApi({}, [
      {
        id: "reasoning-1",
        seq: 6,
        continuation: 1,
        kind: "reasoning",
        role: "assistant",
        text: "Checked the active challenge and prepared the next command.",
        created_at: "2026-08-26T09:00:01Z",
      },
    ]);

    renderPage();

    expect(await screen.findByText("Reasoning")).toBeInTheDocument();
    const row = screen.getByTestId("transcript-reasoning-row");
    expect(row).not.toHaveAttribute("open");
    expect(screen.getByText("Checked the active challenge and prepared the next command.")).toBeInTheDocument();
    expect(screen.queryByTestId("transcript-message-bubble")).not.toBeInTheDocument();
    expect(screen.queryByText("Agent")).not.toBeInTheDocument();
    expect(screen.queryByText("You")).not.toBeInTheDocument();
  });


  it("auto-expands the latest reasoning entry while the Runtime Turn is busy", async () => {
    stubTaskDetailApi({
      status: "running",
      runtime_activity: { liveness: "live", turn_activity: "busy" },
    }, [
      {
        id: "reasoning-old",
        seq: 4,
        continuation: 1,
        kind: "reasoning",
        role: "assistant",
        text: "Older reasoning.",
        created_at: "2026-08-26T09:00:00Z",
      },
      {
        id: "reasoning-live",
        seq: 6,
        continuation: 1,
        kind: "reasoning",
        role: "assistant",
        text: "Checking the auth flow.",
        status: "streaming",
        created_at: "2026-08-26T09:00:01Z",
      },
    ]);

    renderPage();

    const rows = await screen.findAllByTestId("transcript-reasoning-row");
    expect(rows).toHaveLength(2);
    expect(rows[0]).not.toHaveAttribute("open");
    expect(rows[1]).toHaveAttribute("open");
  });


  it("renders a child agent as one collapsed attributed block", async () => {
    stubTaskDetailApi({}, [
      {
        id: "user-msg",
        seq: 1,
        continuation: 1,
        kind: "message",
        role: "user",
        text: "Solve the challenges.",
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "assistant-spawn",
        seq: 2,
        continuation: 1,
        kind: "tool_call",
        role: "assistant",
        tool_call_id: "call_spawn_c06",
        tool_name: "Agent",
        details: { input: { description: "Exploit c-06 HugeGraph" } },
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "subagent-bbr05bd75",
        seq: 12,
        continuation: 1,
        kind: "subagent_block",
        role: "runtime",
        text: "Subagent general-purpose: Exploit c-06 HugeGraph",
        status: "completed",
        tool_name: "claude_code",
        details: {
          subagent_type: "general-purpose",
          spawn_tool_use_id: "call_spawn_c06",
          items: [
            { kind: "tool_call", role: "assistant", id: "child-tool-1", seq: 3, continuation: 1, tool_call_id: "call_child_1", tool_name: "Bash", details: { input: { command: "curl hugegraph" } }, created_at: "2026-01-01T00:00:02Z" },
            { kind: "tool_result", role: "tool", id: "child-result-1", seq: 4, continuation: 1, tool_call_id: "call_child_1", text: "8080 open", created_at: "2026-01-01T00:00:03Z" },
            { kind: "message", role: "assistant", id: "child-msg-1", seq: 5, continuation: 1, text: "hugegraph exploited", created_at: "2026-01-01T00:00:04Z" },
          ],
        },
        created_at: "2026-01-01T00:00:12Z",
      },
    ]);

    renderPage();

    const row = await screen.findByTestId("transcript-subagent-row");
    expect(row).not.toHaveAttribute("open");
    expect(screen.getByText("Subagent general-purpose · completed")).toBeInTheDocument();
    
    const childText = screen.getByText("hugegraph exploited");
    expect(childText).not.toBeVisible();
    await userEvent.click(screen.getByText("Subagent general-purpose · completed"));
    expect(screen.getByText("hugegraph exploited")).toBeVisible();
    
    const toolRow = within(row).getByTestId("transcript-tool-row");
    expect(toolRow).not.toHaveAttribute("open");
    expect(within(toolRow).getByText("Bash · curl hugegraph")).toBeVisible();
    expect(within(toolRow).getByText(/Result · 8080 open/)).toBeVisible();
    expect(toolRow.querySelector("pre")).not.toBeVisible();
    await userEvent.click(within(toolRow).getByText("Bash · curl hugegraph"));
    expect(toolRow.querySelector("pre")).toBeVisible();
  });


  it("keeps agent messages and tool rows tight, only spacing out new user turns", async () => {
    stubTaskDetailApi({}, [
      {
        id: "user-msg",
        seq: 1,
        continuation: 1,
        kind: "message",
        role: "user",
        text: "Inspect the target.",
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "assistant-probe",
        seq: 2,
        continuation: 1,
        kind: "message",
        role: "assistant",
        text: "Running a probe.",
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "assistant-tool-call",
        seq: 2,
        continuation: 1,
        kind: "tool_call",
        role: "assistant",
        tool_call_id: "call-1",
        tool_name: "Bash",
        details: { input: { command: "curl http://localhost:3000" } },
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "tool-result",
        seq: 3,
        continuation: 1,
        kind: "tool_result",
        role: "tool",
        text: "HTTP/1.1 200 OK",
        tool_call_id: "call-1",
        created_at: "2026-01-01T00:00:02Z",
      },
      {
        id: "assistant-summary",
        seq: 4,
        continuation: 1,
        kind: "message",
        role: "assistant",
        text: "The service is up.",
        created_at: "2026-01-01T00:00:03Z",
      },
      {
        id: "next-user-msg",
        seq: 5,
        continuation: 1,
        kind: "message",
        role: "user",
        text: "Thanks, stop there.",
        created_at: "2026-01-01T00:00:04Z",
      },
    ]);

    renderPage();

    const rows = await screen.findAllByTestId("transcript-row");
    expect(rows).toHaveLength(5);
    expect(rows[0]).not.toHaveClass("mt-1");
    expect(rows[0]).not.toHaveClass("mt-4");
    expect(rows[1]).toHaveClass("mt-1");
    expect(rows[2]).toHaveClass("mt-1");
    expect(rows[3]).toHaveClass("mt-1");
    expect(rows[4]).toHaveClass("mt-4");
    expect(screen.getAllByTestId("transcript-turn-header").map((header) => header.textContent)).toEqual(
      expect.arrayContaining([expect.stringContaining("You"), expect.stringContaining("Codex")]),
    );
  });


  it("renders tool call arguments as labeled fields rather than a raw JSON envelope", async () => {
    stubTaskDetailApi({}, [
      {
        id: "assistant-tool-call",
        seq: 7,
        continuation: 1,
        kind: "tool_call",
        role: "assistant",
        tool_call_id: "call-1",
        tool_name: "Bash",
        details: { input: { command: "curl http://localhost:3000", timeout: 30 } },
        created_at: "2026-01-01T00:00:01Z",
      },
    ]);

    renderPage();

    expect(await screen.findByText("Command")).toBeInTheDocument();
    expect(screen.getByText("Timeout")).toBeInTheDocument();
    expect(screen.getByText("30")).toBeInTheDocument();
    expect(screen.queryByText(/"input":/)).not.toBeInTheDocument();
  });
});
