import { LocationProbe } from "@/test/RouteControls";
import { render } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { vi } from "vitest";
import { mockApi } from "@/test/mockApi";
import { TaskDetailPage } from "./TaskDetailPage";

export function setDocumentVisibility(value: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { value, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

export function mockConversationViewport(options: {
  scrollHeight: () => number;
  clientHeight?: () => number;
  scrollTo?: (this: HTMLElement, options: ScrollToOptions) => void;
}) {
  const scrollHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
  const clientHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
  const scrollToDescriptor = Object.getOwnPropertyDescriptor(Element.prototype, "scrollTo");
  const clientHeight = options.clientHeight ?? (() => 600);
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", { get: options.scrollHeight, configurable: true });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", { get: clientHeight, configurable: true });
  Object.defineProperty(Element.prototype, "scrollTo", {
    configurable: true,
    value: options.scrollTo ?? function (this: HTMLElement, scrollOptions: ScrollToOptions) {
      this.scrollTop = Math.max(0, Math.min(scrollOptions.top ?? 0, this.scrollHeight - this.clientHeight));
      this.dispatchEvent(new Event("scroll"));
    },
  });

  return () => {
    restoreProperty(HTMLElement.prototype, "scrollHeight", scrollHeightDescriptor);
    restoreProperty(HTMLElement.prototype, "clientHeight", clientHeightDescriptor);
    restoreProperty(Element.prototype, "scrollTo", scrollToDescriptor);
  };
}

export function restoreProperty(target: object, property: PropertyKey, descriptor?: PropertyDescriptor) {
  if (descriptor) {
    Object.defineProperty(target, property, descriptor);
    return;
  }
  Reflect.deleteProperty(target, property);
}

export function renderPage(initialEntry = "/projects/project-1/tasks/task-1", onSearch?: (search: string) => void) {
  return render(
    <StrictMode>
      <MemoryRouter initialEntries={[initialEntry]}>
        {onSearch && <LocationProbe onChange={onSearch} />}
        <Routes>
          <Route path="/projects/:projectId/tasks/:taskId" element={<TaskDetailPage />} />
          <Route path="/projects/:projectId/tasks" element={<div>Task list</div>} />
        </Routes>
      </MemoryRouter>
    </StrictMode>,
  );
}

export function stubTaskDetailApi(
  taskOverrides: Record<string, unknown> = {},
  transcriptEntries: Record<string, unknown>[] = [
    {
      id: "entry-1",
      seq: 1,
      continuation: 1,
      kind: "message",
      role: "assistant",
      text: "Conversation should be hidden by default",
      created_at: "2026-01-01T00:00:00Z",
    },
  ],
  timelineBody: Record<string, unknown> = {
    task_id: "task-1",
    items: [{ seq: 1, type: "text", content: "Timeline opened first", created_at: "2026-01-01T00:00:00Z" }],
  },
  transcriptBody: Record<string, unknown> = {
    task_id: "task-1",
    entries: transcriptEntries,
  },
  extraRoutes: Record<string, unknown> = {},
) {
  const scrollIntoView = vi.fn();
  Object.defineProperty(Element.prototype, "scrollIntoView", {
    value: scrollIntoView,
    configurable: true,
  });

  const fetchMock = mockApi({
    
	"/api/projects/project-1/tasks/task-1/finish-readiness": { ready_to_finish: true, blockers: [] },
    ...extraRoutes,
    "/api/projects/project-1/tasks/task-1/timeline": timelineBody,
    "/api/projects/project-1/tasks/task-1/transcript": transcriptBody,
    "/api/projects/project-1/tasks/task-1": {
      id: "task-1",
      project_id: "project-1",
      goal: "Inspect task view",
      status: "completed",
      runner: "sandbox",
      runtime_profile_id: "profile-1",
      run_controls: {},
      scope_snapshot: {},
      runtime_controls: {
        native_resume_available: true,
        resume_available: true,
        queue_steer_available: true,
        interrupt_steer_available: false,
        native_session_captured: true,
        same_runtime_provider_only: true,
        runtime_provider: "codex",
      },
      latest_continuation: {
        id: "cont-1",
        task_id: "task-1",
        number: 1,
        runtime_profile_id: "profile-1",
        runtime_provider: "codex",
        runner: "sandbox",
        status: "completed",
        native_session_id: "sess-1",
        started_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:05Z",
        ended_at: "2026-01-01T00:00:05Z",
      },
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:05Z",
      ...taskOverrides,
    },
    "/api/runtime-profiles": {
      profiles: [
        { id: "profile-1", name: "Codex", provider: "codex", fields: {} },
        { id: "profile-2", name: "Fake", provider: "fake", fields: {} },
      ],
    },
    "/api/model-providers": {
      providers: [
        {
          id: "mimo",
          name: "MiMo",
          base_url: "https://api.example.test/v1",
          protocols: ["openai_responses"],
          api_key_env: "MIMO_API_KEY",
          catalog: { manual: ["mimo-v2-flash", "mimo-v2-pro"], default_model: "mimo-v2-flash" },
        },
        {
          id: "anthropic",
          name: "Anthropic",
          base_url: "https://api.anthropic.test/v1",
          protocols: ["anthropic_messages"],
          api_key_env: "ANTHROPIC_API_KEY",
          catalog: { manual: ["claude-sonnet", "claude-opus"], default_model: "claude-sonnet" },
        },
      ],
    },
    "/api/runtime-plugins": {
      plugins: [
        {
          schema_version: 1,
          id: "codex",
          name: "Codex",
          binary: { default: "codex" },
          capabilities: { sandbox: true, host: true, mcp_config: true, streaming_transcript: true, resume: true },
          model_provider: {
            requirement: "required",
            supported_protocols: ["openai_responses"],
            protocol_preference: ["openai_responses"],
          },
          profile_schema: { fields: [] },
          config_projection: { primitive: "codex" },
          launch: { args: [] },
          transcript: { parser: "codex" },
        },
        {
          schema_version: 1,
          id: "pi",
          name: "Pi",
          binary: { default: "pi" },
          capabilities: {
            sandbox: true,
            host: true,
            mcp_config: true,
            streaming_transcript: true,
            resume: true,
            persistent_session: true,
            send_turn: true,
            in_turn_steer: true,
          },
          model_provider: {
            requirement: "required",
            supported_protocols: ["openai_chat_completions", "openai_responses", "anthropic_messages"],
            protocol_preference: ["openai_chat_completions", "openai_responses", "anthropic_messages"],
          },
          profile_schema: { fields: [] },
          config_projection: { primitive: "pi_agent" },
          launch: { args: [] },
          transcript: { parser: "pi" },
        },
      ],
    },
  });

  return { fetchMock, scrollIntoView };
}
