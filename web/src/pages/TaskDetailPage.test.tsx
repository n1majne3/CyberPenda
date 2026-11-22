import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage, stubTaskDetailApi } from "./TaskDetailPage.testSupport";

describe("TaskDetailPage", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });


  it("opens on the interactive conversation by default", async () => {
    stubTaskDetailApi();

    renderPage();

    const tabs = await screen.findAllByRole("button", { name: /^(Timeline|Conversation)$/ });
    expect(tabs.map((tab) => tab.textContent?.trim())).toEqual(["Conversation", "Timeline"]);
    expect(tabs[0]).toHaveAttribute("aria-pressed", "true");
    expect(tabs[1]).toHaveAttribute("aria-pressed", "false");
    expect(tabs[0]?.querySelector("svg")).toBeNull();
    expect(tabs[1]?.querySelector("svg")).toBeNull();
    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    expect(screen.getByTestId("conversation-workspace")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Task message" })).toBeInTheDocument();
    expect(screen.getByLabelText("Continuation model provider")).toHaveClass("appearance-none");
    expect(screen.getByLabelText("Continuation model")).toHaveClass("appearance-none");
    expect(screen.getByLabelText("Continuation reasoning effort")).toHaveClass("appearance-none");
    expect(screen.getByTestId("task-workspace")).toHaveClass("overflow-visible", "md:overflow-hidden");
    expect(screen.getByTestId("task-composer")).toHaveClass("fixed", "inset-x-0", "bottom-0", "md:static");
    expect(screen.getByTestId("task-detail-shell")).toHaveClass("mx-auto", "max-w-[1280px]");
    expect(screen.getByTestId("conversation-workspace")).toHaveClass("pb-44", "md:pb-5");
    expect(screen.getByLabelText("Continuation reasoning effort").parentElement).toHaveClass("w-[5.25rem]", "shrink-0");
    expect(screen.queryByText(/Shift\+Enter for a new line/i)).not.toBeInTheDocument();
    expect(screen.getByTestId("composer-send").querySelector(".lucide-arrow-up")).not.toBeNull();
    expect(screen.queryByText("Timeline opened first")).not.toBeInTheDocument();
  });


  it("uses a document heading and open conversation layout", async () => {
    stubTaskDetailApi();
    renderPage();
    expect(await screen.findByRole("heading", { name: "Inspect task view" })).toHaveClass("text-[29px]");
    expect(screen.getByTestId("task-session-header")).not.toHaveClass("border");
    expect(screen.getByRole("button", { name: "Conversation" }).parentElement).toHaveClass("bg-muted/60");
    expect(screen.getByTestId("task-workspace")).not.toHaveClass("border", "rounded-b-xl");
    expect(screen.getByTestId("task-composer")).not.toHaveClass("border-t");
  });


  it("shows pending Working Graph settlement state in the Task header", async () => {
    stubTaskDetailApi({
      blackboard_conclusion: {
        mode: "working_graph",
        state: "pending",
        source_turn_id: "turn-7",
      },
    });

    renderPage();

    const badge = await screen.findByTestId("blackboard-conclusion-state");
    expect(badge).toHaveTextContent("Blackboard · Working Graph · pending");
    expect(badge).toHaveAttribute("title", expect.stringContaining("turn-7"));
  });


  it("shows Disabled mode and hides Task Blackboard surfaces", async () => {
    stubTaskDetailApi({
      status: "running",
      run_controls: { blackboard_mode: "disabled" },
      blackboard_conclusion: {
        mode: "disabled",
        state: "action_required",
        error_code: "semantic_conclusion_repair_exhausted",
        retry_available: true,
      },
    });

    renderPage();

    expect(await screen.findByTestId("blackboard-conclusion-state")).toHaveTextContent("Blackboard: Disabled");
    expect(screen.queryByRole("alert", { name: "Blackboard conclusion requires attention" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Conversation" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Timeline" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Task message" })).toBeInTheDocument();
  });


  it("shows every Finish Readiness blocker and its related surface on Task Detail", async () => {
	stubTaskDetailApi({}, undefined, undefined, undefined, {
	  "/api/projects/project-1/tasks/task-1/finish-readiness": {
		ready_to_finish: false,
		blockers: [
		  {
			code: "blackboard_conclusion_action_required",
			count: 1,
			message: "A Blackboard conclusion needs operator action.",
			links: ["/projects/project-1/blackboard"],
		  },
		  {
			code: "unfinalized_challenge_attempts",
			count: 2,
			message: "Challenge Attempts are not finalized.",
			links: ["/projects/project-1/tasks/task-1/challenges"],
		  },
		],
	  },
	});

	renderPage();

	const readiness = await screen.findByRole("region", { name: "Finish Readiness" });
	expect(within(readiness).getByText("A Blackboard conclusion needs operator action.")).toBeInTheDocument();
	expect(within(readiness).getByText("Challenge Attempts are not finalized.")).toBeInTheDocument();
	expect(within(readiness).getByRole("link", { name: "Open blackboard_conclusion_action_required" })).toHaveAttribute("href", "/projects/project-1/blackboard");
	expect(within(readiness).getByRole("link", { name: "Open unfinalized_challenge_attempts" })).toHaveAttribute("href", "/projects/project-1/tasks/task-1/challenges");
  });


  it("shows Working Graph settlement in the Task header", async () => {
	stubTaskDetailApi({
	  blackboard_conclusion: {
		mode: "working_graph",
		state: "concluding",
		source_turn_id: "turn-7",
	  },
	});

	renderPage();
	expect(await screen.findByTestId("blackboard-conclusion-state")).toHaveTextContent("Blackboard · Working Graph · concluding");
  });


  it("shows the applied Blackboard revision in the Task header", async () => {
	stubTaskDetailApi({
	  blackboard_conclusion: { mode: "working_graph", state: "clean", source_turn_id: "turn-7", applied_revision: 9 },
	});
	renderPage();
	const badge = await screen.findByTestId("blackboard-conclusion-state");
	expect(badge).toHaveTextContent("applied revision 9");
	expect(badge).toHaveAttribute("title", expect.stringContaining("revision 9"));
  });


  it("surfaces action-required Blackboard recovery without blocking manual conversation", async () => {
    const { fetchMock } = stubTaskDetailApi({
      status: "running",
      blackboard_conclusion: {
        mode: "working_graph",
        state: "action_required",
        source_turn_id: "turn-7",
        error_code: "semantic_conclusion_invalid_result",
        retry_available: true,
      },
    });

    renderPage();

    expect(await screen.findByTestId("blackboard-conclusion-state")).toHaveTextContent(
      "Blackboard · Working Graph · action required",
    );
    expect(screen.getByRole("textbox", { name: "Task message" })).toBeEnabled();
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("blackboard-conclusion/retry"))).toBe(false);
  });


  it("deep-links and updates the task view tab", async () => {
    const searches: string[] = [];
    const user = userEvent.setup();
    stubTaskDetailApi();

    renderPage("/projects/project-1/tasks/task-1?view=conversation", (search) => searches.push(search));

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Conversation" })).toHaveAttribute("aria-pressed", "true");
    // The virtual window's measured coverage pass needs real row heights, so
    // rows must not carry content-visibility size hints.
    expect(screen.getByTestId("transcript-row")).not.toHaveClass("[content-visibility:auto]");

    await user.click(screen.getByRole("button", { name: "Timeline" }));
    expect(searches.at(-1)).toBe("?view=timeline");
  });


  it("switches into a compact focus view without project chrome", async () => {
    const searches: string[] = [];
    const user = userEvent.setup();
    stubTaskDetailApi();

    renderPage("/projects/project-1/tasks/task-1", (search) => searches.push(search));

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Project sections" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Enter focus view" }));

    expect(searches.at(-1)).toBe("?focus=1");
    expect(screen.queryByRole("navigation", { name: "Project sections" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Exit focus view" })).toBeInTheDocument();
    expect(screen.getByTestId("task-session-header")).toHaveClass("py-2");
    expect(screen.getByTestId("task-detail-shell")).toHaveClass("h-full", "p-0");
  });


  it("gives task tabs focus rings and names the auto-follow state", async () => {
    stubTaskDetailApi();

    renderPage();

    expect(await screen.findByText("Conversation should be hidden by default")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Timeline" })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("button", { name: "Conversation" })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("button", { name: "Scroll to top" })).toHaveClass("focus-visible:ring-2");
    expect(screen.getByRole("button", { name: /Scroll to latest \(auto-follow on\)/i })).toHaveClass(
      "h-8",
      "w-8",
    );
  });


  it("shows the compact continuation summary when present", async () => {
    stubTaskDetailApi();

    renderPage();

    const summary = await screen.findByTestId("continuation-summary");
    expect(summary).toHaveTextContent("continuation #1");
    expect(summary).toHaveTextContent("runtime codex");
    expect(summary).toHaveTextContent("runner docker");
    expect(summary).not.toHaveTextContent("continuation status");
    expect(summary).not.toHaveTextContent("native session");
    expect(summary).toHaveAttribute("title", expect.stringContaining("status: completed"));
    expect(summary).toHaveAttribute("title", expect.stringContaining("native session: captured"));
  });


  it("shows the prior terminal continuation next to the current writable one", async () => {
    stubTaskDetailApi({
      status: "running",
      active_continuation: {
        id: "cont-1",
        task_id: "task-1",
        number: 1,
        runtime_profile_id: "profile-1",
        runtime_provider: "codex",
        runner: "sandbox",
        status: "running",
      },
      latest_continuation: {
        id: "cont-2",
        task_id: "task-1",
        number: 2,
        runtime_profile_id: "profile-1",
        runtime_provider: "codex",
        runner: "sandbox",
        status: "completed",
      },
    });

    renderPage();

    expect(await screen.findByText("continuation #1")).toBeInTheDocument();
    const summary = screen.getByTestId("continuation-summary");
    expect(summary).not.toHaveTextContent("continuation status");
    expect(summary).toHaveAttribute("title", expect.stringContaining("prior terminal: #2 (completed)"));
  });


  it("does not repeat the same continuation as its own prior terminal", async () => {
    stubTaskDetailApi({
      status: "running",
      active_continuation: {
        id: "cont-2",
        task_id: "task-1",
        number: 2,
        runtime_profile_id: "profile-1",
        runtime_provider: "codex",
        runner: "sandbox",
        status: "running",
      },
      latest_continuation: {
        id: "cont-2",
        task_id: "task-1",
        number: 2,
        runtime_profile_id: "profile-1",
        runtime_provider: "codex",
        runner: "sandbox",
        status: "running",
      },
    });

    renderPage();

    const summary = await screen.findByTestId("continuation-summary");
    expect(summary).toHaveTextContent("continuation #2");
    expect(summary).not.toHaveTextContent("continuation status");
    expect(summary).toHaveAttribute("title", expect.not.stringContaining("prior terminal"));
  });


  it("keeps the compact continuation summary bounded below the title row", async () => {
    stubTaskDetailApi();

    renderPage();

    expect(await screen.findByText("continuation #1")).toBeInTheDocument();
    const summary = screen.getByTestId("continuation-summary");
    expect(summary).toHaveClass("min-w-0", "overflow-hidden", "whitespace-nowrap");
    const blackboardChip = screen.getByTestId("blackboard-conclusion-state");
    expect(blackboardChip).toHaveClass("min-w-0", "shrink");
  });


  it("shows Runtime Activity separately from Task lifecycle", async () => {
    stubTaskDetailApi({
      status: "running",
      runtime_activity: { liveness: "live", turn_activity: "busy" },
    });

    renderPage();

    expect(await screen.findByTestId("runtime-activity")).toHaveTextContent(/Runtime busy/i);
    expect(screen.getByText("Running")).toBeInTheDocument();
  });
});

describe("Challenge Workflow entry", () => {

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("hides Challenge history when the Task has no retained records", async () => {
    stubTaskDetailApi({ type: "pentest", challenge_history_available: false });
    renderPage();
    expect(await screen.findByText("Inspect task view")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Challenge history" })).not.toBeInTheDocument();
  });

  it("shows Challenge history for a Task with retained records", async () => {
    stubTaskDetailApi({ type: "ctf_challenge", challenge_history_available: true });
    renderPage();
    expect(await screen.findByRole("button", { name: "Challenge history" })).toBeInTheDocument();
  });
});
