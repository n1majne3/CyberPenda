# Runtime Transcript Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a full readable task runtime transcript derived from retained task events.

**Architecture:** Add a small backend transcript projection package that turns task goal, steering events, lifecycle boundaries, and runtime output events into typed transcript entries. Expose it through `GET /api/projects/{project_id}/tasks/{task_id}/transcript`, then add a Conversation tab to the task detail page while preserving the existing Timeline tab.

**Tech Stack:** Go HTTP handlers and task service, React + TypeScript + lucide-react UI, existing Vite build and Go test setup.

---

## File Structure

- Create `internal/transcript/transcript.go`: transcript entry types, parser interface, provider-aware event projection, and generic runtime-output fallback behavior.
- Create `internal/transcript/transcript_test.go`: unit tests for task goals, continuation boundaries, steering, provider-specific parsing, tool-call grouping, and fallback runtime output.
- Modify `internal/daemon/server.go`: register the transcript HTTP route.
- Modify `internal/daemon/task_handlers.go`: add `handleTaskTranscript` using the task ownership checks already used by `handleTaskEvents`.
- Modify `internal/daemon/task_test.go`: HTTP coverage that proves existing tasks can read transcript entries and wrong-project access is rejected.
- Modify `web/src/lib/api.ts`: add `TaskTranscript` and `TaskTranscriptEntry` interfaces.
- Modify `web/src/pages/TaskDetailPage.tsx`: load transcript with events, add Conversation/Timeline tabs, render transcript entries, and collapse tool/runtime details by default.
- Rebuild `internal/daemon/webfs/dist` through `make build-ui`.

## Task 1: Backend Transcript Projection

**Files:**
- Create: `internal/transcript/transcript.go`
- Test: `internal/transcript/transcript_test.go`

- [ ] **Step 1: Write failing transcript projection tests**

```go
func TestBuildIncludesGoalContinuationsSteeringAndFallback(t *testing.T) {
    createdAt := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
    subject := task.Task{ID: "task-1", Goal: "Recon Juice Shop", CreatedAt: createdAt}
    events := []task.Event{
        {ID: "ev-1", Seq: 1, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "started", "adapter": "pi"}, CreatedAt: createdAt.Add(time.Second)},
        {ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"stream": "stdout", "text": "plain line"}, CreatedAt: createdAt.Add(2 * time.Second)},
        {ID: "ev-3", Seq: 3, Kind: task.EventKindSteering, Payload: task.EventPayload{"directive": "Focus admin"}, CreatedAt: createdAt.Add(3 * time.Second)},
    }

    got := transcript.Build(subject, events)

    requireEntry(t, got, "task-task-1-goal", "message", "user", "Recon Juice Shop")
    requireEntry(t, got, "ev-1-continuation", "continuation", "system", "Continuation #1 started with pi")
    fallback := requireEntry(t, got, "ev-2-runtime", "runtime_output", "runtime", "plain line")
    if fallback.Stream != "stdout" || fallback.Status != "collapsed" {
        t.Fatalf("expected collapsed stdout fallback, got %#v", fallback)
    }
    requireEntry(t, got, "ev-3-steering", "message", "user", "Focus admin")
}

func TestBuildParsesOpenAIToolCallAndResult(t *testing.T) {
    subject := task.Task{ID: "task-1", Goal: "Do work", CreatedAt: time.Now().UTC()}
    events := []task.Event{
        {ID: "ev-1", Seq: 1, Kind: task.EventKindLifecycle, Payload: task.EventPayload{"phase": "started", "adapter": "pi"}},
        {ID: "ev-2", Seq: 2, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"tool_call","id":"call-1","name":"curl","arguments":{"url":"http://127.0.0.1:3000"}}`}},
        {ID: "ev-3", Seq: 3, Kind: task.EventKindRuntimeOutput, Payload: task.EventPayload{"text": `{"type":"tool_result","tool_call_id":"call-1","output":"200 OK"}`}},
    }

    got := transcript.Build(subject, events)

    call := requireKindSeq(t, got, "ev-2-tool-call", "tool_call")
    if call.ToolCallID != "call-1" || call.ToolName != "curl" || call.Status != "collapsed" {
        t.Fatalf("unexpected tool call: %#v", call)
    }
    result := requireKindSeq(t, got, "ev-3-tool-result", "tool_result")
    if result.ToolCallID != "call-1" || result.Text != "200 OK" || result.Status != "collapsed" {
        t.Fatalf("unexpected tool result: %#v", result)
    }
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/transcript`

Expected: FAIL because `internal/transcript` does not exist.

- [ ] **Step 3: Implement projection types and parsers**

```go
type Entry struct {
    ID           string         `json:"id"`
    Seq          int            `json:"seq"`
    Continuation int            `json:"continuation"`
    Kind         string         `json:"kind"`
    Role         string         `json:"role"`
    Text         string         `json:"text,omitempty"`
    ToolCallID   string         `json:"tool_call_id,omitempty"`
    ToolName     string         `json:"tool_name,omitempty"`
    Details      map[string]any `json:"details,omitempty"`
    Stream       string         `json:"stream,omitempty"`
    Status       string         `json:"status,omitempty"`
    CreatedAt    time.Time      `json:"created_at"`
}

func Build(subject task.Task, events []task.Event) []Entry {
    entries := []Entry{goalEntry(subject)}
    continuation := 0
    adapter := ""
    for _, event := range events {
        if event.Kind == task.EventKindLifecycle && stringValue(event.Payload, "phase") == "started" {
            continuation++
            adapter = stringValue(event.Payload, "adapter")
            entries = append(entries, continuationEntry(event, continuation, adapter))
            continue
        }
        entries = append(entries, entriesForEvent(event, continuation, adapter)...)
    }
    return entries
}
```

Implement `entriesForEvent` so:

- `steering.directive` becomes `kind=message`, `role=user`.
- `conversation.role/text` becomes `kind=message` with that role.
- Runtime JSON with `type=message|assistant_message|response.output_text` becomes `kind=message`, `role=assistant`.
- Runtime JSON with `type=tool_call|function_call` becomes `kind=tool_call`, collapsed.
- Runtime JSON with `type=tool_result|function_call_output` becomes `kind=tool_result`, collapsed.
- Any unknown runtime output becomes `kind=runtime_output`, `role=runtime`, collapsed, preserving `stream`.

- [ ] **Step 4: Run transcript unit tests**

Run: `go test ./internal/transcript`

Expected: PASS.

## Task 2: Transcript HTTP Endpoint

**Files:**
- Modify: `internal/daemon/server.go`
- Modify: `internal/daemon/task_handlers.go`
- Test: `internal/daemon/task_test.go`

- [ ] **Step 1: Write failing handler tests**

```go
func TestTaskTranscriptEndpointProjectsRetainedEvents(t *testing.T) {
    server := newDaemon(t)
    projectID := createProject(t, server, `{"name":"Acme","scope":{"domains":["example.com"]}}`)
    profileID := createRuntimeProfile(t, server, `{"name":"Fake","provider":"fake"}`)
    taskID := createTask(t, server, projectID, `{"goal":"map app","runtime_profile_id":`+quoteJSON(profileID)+`,"runner":"sandbox"}`)

    waitForEventText(t, server, projectID, taskID, "enumerating in-scope assets")

    resp := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID+"/transcript", nil)
    server.ServeHTTP(resp, req)

    if resp.Code != http.StatusOK {
        t.Fatalf("expected transcript status 200, got %d with body %s", resp.Code, resp.Body.String())
    }
    var body struct {
        TaskID  string              `json:"task_id"`
        Entries []map[string]any    `json:"entries"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
        t.Fatalf("decode transcript: %v", err)
    }
    if body.TaskID != taskID || len(body.Entries) == 0 {
        t.Fatalf("expected entries for task, got %#v", body)
    }
}
```

- [ ] **Step 2: Run handler test to verify failure**

Run: `go test ./internal/daemon -run TestTaskTranscriptEndpointProjectsRetainedEvents -count=1`

Expected: FAIL with 404 for missing route.

- [ ] **Step 3: Register route and handler**

Add this route next to `/events`:

```go
server.mux.HandleFunc("GET /api/projects/{id}/tasks/{task_id}/transcript", server.handleTaskTranscript)
```

Add handler:

```go
func (server *Server) handleTaskTranscript(response http.ResponseWriter, request *http.Request) {
    found, ok := server.requireProjectTask(response, request)
    if !ok {
        return
    }
    events, err := server.tasks.Events(found.ID)
    if err != nil {
        writeError(response, http.StatusInternalServerError, "list task events")
        return
    }
    entries := transcript.Build(found, events)
    if entries == nil {
        entries = []transcript.Entry{}
    }
    writeJSON(response, http.StatusOK, struct {
        TaskID  string             `json:"task_id"`
        Entries []transcript.Entry `json:"entries"`
    }{TaskID: found.ID, Entries: entries})
}
```

If no shared `requireProjectTask` helper exists, add one in `task_handlers.go` and switch `handleGetTask`, `handleTaskEvents`, and `handleTaskTranscript` to use it.

- [ ] **Step 4: Run daemon tests**

Run: `go test ./internal/daemon -run 'TestTaskTranscript|TestLaunchTaskRunsFakeRuntimeAndStreamsEvents' -count=1`

Expected: PASS.

## Task 3: Task Detail Conversation UI

**Files:**
- Modify: `web/src/lib/api.ts`
- Modify: `web/src/pages/TaskDetailPage.tsx`

- [ ] **Step 1: Add TypeScript API types**

```ts
export interface TaskTranscriptEntry {
  id: string;
  seq: number;
  continuation: number;
  kind: "message" | "tool_call" | "tool_result" | "runtime_output" | "continuation" | string;
  role: "user" | "assistant" | "system" | "runtime" | string;
  text?: string;
  tool_call_id?: string;
  tool_name?: string;
  details?: Record<string, unknown>;
  stream?: string;
  status?: string;
  created_at: string;
}

export interface TaskTranscript {
  task_id: string;
  entries: TaskTranscriptEntry[];
}
```

- [ ] **Step 2: Load transcript with task and event data**

In `TaskDetailPage.tsx`, add `transcript` and `activeView` state:

```ts
const [transcript, setTranscript] = useState<TaskTranscriptEntry[]>([]);
const [activeView, setActiveView] = useState<"conversation" | "timeline">("conversation");
```

Extend `loadAll`:

```ts
const [t, ev, tr] = await Promise.all([
  apiGet<Task>(`${base}`),
  apiGet<{ events: TaskEvent[] }>(`${base}/events`),
  apiGet<TaskTranscript>(`${base}/transcript`),
]);
setTranscript(tr.entries ?? []);
```

- [ ] **Step 3: Render tabs and transcript entries**

Replace the always-visible Timeline block with:

```tsx
<div className="flex items-center gap-1 border-b border-border mb-3">
  <button className={tabClass(activeView === "conversation")} onClick={() => setActiveView("conversation")}>
    <MessageSquare className="h-4 w-4" /> Conversation
  </button>
  <button className={tabClass(activeView === "timeline")} onClick={() => setActiveView("timeline")}>
    <Activity className="h-4 w-4" /> Timeline
  </button>
</div>
{activeView === "conversation" ? (
  <TranscriptList entries={transcript} endRef={timelineEnd} />
) : (
  <TimelineList events={events} endRef={timelineEnd} />
)}
```

Use `<details>` for `tool_call`, `tool_result`, and `runtime_output` entries so they are collapsed by default.

- [ ] **Step 4: Run TypeScript build**

Run: `cd web && npm run build`

Expected: PASS.

## Task 4: Embedded UI Assets and Full Verification

**Files:**
- Modify: `internal/daemon/webfs/dist/**`

- [ ] **Step 1: Rebuild embedded UI**

Run: `make build-ui`

Expected: Vite build succeeds and `internal/daemon/webfs/dist` updates.

- [ ] **Step 2: Run focused backend tests**

Run: `go test ./internal/transcript ./internal/daemon -count=1`

Expected: PASS.

- [ ] **Step 3: Run full backend test suite**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 4: Review final diff**

Run: `git diff --check`

Expected: no whitespace errors.

Run: `git status --short`

Expected: only transcript implementation files and rebuilt assets are dirty.

## Self-Review

- Spec coverage: backend projection endpoint, task detail Conversation default, Timeline preservation, collapsed tool/runtime details, historical event compatibility, and no schema migration are covered by Tasks 1-4.
- Placeholder scan: no TBD/TODO/fill-in-later placeholders are present.
- Type consistency: Go response uses `entries` and TypeScript consumes `TaskTranscript.entries`; entry fields match the design spec.
