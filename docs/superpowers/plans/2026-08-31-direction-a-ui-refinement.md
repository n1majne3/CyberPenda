# Direction A UI Refinement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply the complete Direction A (refined minimal) mockup design to the production React codebase across all 7 pages: Workspace, Launch, Dashboard, Profiles, Providers, Credentials, Skills.

**Architecture:** Modify existing page components in-place using established patterns (`SettingsPageShell`, `SettingsSplitLayout`, `SettingsPageHeader`, `Card`, `Button`, `Badge`). No new dependencies. All changes are presentational — no API or state logic changes unless noted.

**Tech Stack:** React 19, TypeScript, Tailwind CSS 3.4, CVA, lucide-react, Vitest + Testing Library.

---

## Design Token Reference

All changes use existing production tokens from `web/src/index.css`:
- `--signal` (teal): semantic accent for mode/classification chips
- `--success` (green): running/active states
- `--info` (blue): idle states
- `--warning` (amber): attention needed
- `--destructive` (red): offline/error states
- `--radius`: 0.375rem (rounded-md), 0.5rem (rounded-lg), 0.75rem (rounded-xl)

---

## File Structure

| File | Changes |
|---|---|
| `web/src/components/ui.tsx` | Add `Chip` component (semantic variants: neutral/success/info/warning/danger/signal) |
| `web/src/components/shared.tsx` | Add `SectionLabel` component (uppercase tracking-wider text-xs) |
| `web/src/components/WorkspaceSidebar.tsx` | Add search filter, session status dots, relative time for duplicate names, group counts |
| `web/src/components/task-transcript/AgentTranscriptView.tsx` | Turn-grouped timeline, single-line tool calls, collapsible results, reasoning fold, system event dividers, assistant message left-border, composer status banner |
| `web/src/components/RuntimeLaunchControls.tsx` | Reasoning effort segmented control, advanced config accordions with value summaries |
| `web/src/pages/TaskLaunchPage.tsx` | Remove subtitle, hero goal input, 2×2 common config card, advanced accordions, right sticky launch summary |
| `web/src/pages/ProjectDashboardPage.tsx` | Scope readiness checklist, stat cards with state breakdowns, Recent activity + Current work sections, pill tabs |
| `web/src/pages/RuntimeProfilesPage.tsx` | Remove subtitle, grouped preset vs launch-resolved lists, selected-state highlight, top-fixed save/delete, 2-card form, capability chips, API key indicator, segmented effort |
| `web/src/pages/ModelProvidersPage.tsx` | Remove subtitle, key status dots + protocol chips in list, header badges, protocol checkbox rows, catalog toolbar, 2-card form, separated save/delete |
| `web/src/pages/CredentialBindingsPage.tsx` | Remove subtitle, stats header, segmented filters + search, table with column headers, provider links, right action panel |
| `web/src/pages/SkillsPage.tsx` | Remove subtitle, profile view selector card, stats + segmented filters, table with toggle switches, right fixed upload/edit panel, import security note |

---

## Task 1: Add Chip and SectionLabel shared components

**Files:**
- Modify: `web/src/components/ui.tsx` (add Chip)
- Modify: `web/src/components/shared.tsx` (add SectionLabel)

- [x] **Step 1: Add Chip component to ui.tsx**

Add after the Badge component:

```tsx
const chipVariants = cva(
  "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium",
  {
    variants: {
      variant: {
        neutral: "border-border bg-muted text-muted-foreground",
        success: "border-success/25 bg-success/10 text-success",
        info: "border-info/25 bg-info/10 text-info",
        warning: "border-warning/25 bg-warning/10 text-warning",
        danger: "border-destructive/25 bg-destructive/10 text-destructive",
        signal: "border-signal/25 bg-signal/10 text-signal",
      },
    },
    defaultVariants: { variant: "neutral" },
  },
);
export interface ChipProps extends HTMLAttributes<HTMLSpanElement>, VariantProps<typeof chipVariants> {
  dot?: boolean;
}
export function Chip({ className, variant, dot, children, ...props }: ChipProps) {
  return (
    <span className={cn(chipVariants({ variant }), className)} {...props}>
      {dot && <span className="h-1.5 w-1.5 rounded-full bg-current" />}
      {children}
    </span>
  );
}
```

- [x] **Step 2: Add SectionLabel to shared.tsx**

Add after `SettingsPageHeader`:

```tsx
export function SectionLabel({ className, children, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return (
    <p className={cn("font-mono text-xs uppercase tracking-[0.14em] text-muted-foreground", className)} {...props}>
      {children}
    </p>
  );
}
```

- [x] **Step 3: Verify**

Run: `cd web && npx vitest run --grep "ui|shared" -v`
Expected: PASS

- [x] **Step 4: Commit**

```bash
git add web/src/components/ui.tsx web/src/components/shared.tsx
git commit -m "feat(ui): add Chip and SectionLabel components for Direction A"
```

---

## Task 2: Enhance WorkspaceSidebar with search, status dots, and group counts

**Files:**
- Modify: `web/src/components/WorkspaceSidebar.tsx`

- [x] **Step 1: Add search filter input**

Add state and search input at the top of the sidebar:

```tsx
const [searchQuery, setSearchQuery] = useState("");

// In the sidebar header area (before the nav tree):
<div className="px-3 pt-3">
  <div className="flex items-center gap-2 rounded-md border border-input bg-background px-2 h-8">
    <Search className="h-3.5 w-3.5 text-muted-foreground" />
    <input
      placeholder="Filter…"
      value={searchQuery}
      onChange={(e) => setSearchQuery(e.target.value)}
      className="w-full bg-transparent text-xs outline-none placeholder:text-muted-foreground"
    />
  </div>
</div>
```

Filter sessions and projects by name using `searchQuery.toLowerCase()`.

- [x] **Step 2: Add session status dots**

For each session item, add a colored dot indicating Runtime activity state:

```tsx
<span className={cn(
  "h-1.5 w-1.5 rounded-full flex-none",
  session.runtimeActivity === "busy" && "bg-success animate-pulse",
  session.runtimeActivity === "idle" && "bg-info",
  (!session.runtimeActivity || session.runtimeActivity === "offline") && "bg-muted-foreground/40",
)} />
```

- [x] **Step 3: Add relative time for duplicate session names**

When multiple sessions share the same name, append relative time:

```tsx
const nameCount = sessions.reduce((acc, s) => { acc[s.name] = (acc[s.name] || 0) + 1; return acc; }, {});
// In the session name renderer:
<span className="truncate">{session.name}</span>
{nameCount[session.name] > 1 && (
  <span className="ml-1 text-xs text-muted-foreground">{formatRelativeTime(session.updatedAt)}</span>
)}
```

- [x] **Step 4: Add group counts**

Show count badges on "Non-project" and "Projects" group headers:

```tsx
<span className="text-xs text-muted-foreground">{sessions.length}</span>
```

- [x] **Step 5: Verify**

Run: `cd web && npx vitest run --grep "WorkspaceSidebar" -v`
Expected: PASS

- [x] **Step 6: Commit**

```bash
git add web/src/components/WorkspaceSidebar.tsx
git commit -m "feat(sidebar): add search, status dots, relative time, and group counts per Direction A"
```

---

## Task 3: Restructure AgentTranscriptView with Turn-grouped timeline

**Files:**
- Modify: `web/src/components/task-transcript/AgentTranscriptView.tsx`

- [x] **Step 1: Add TurnGroup wrapper component**

```tsx
function TurnGroup({ children, marker }: { children: ReactNode; marker: "user" | "runtime" | "system" }) {
  return (
    <section className="relative pl-6">
      <span className={cn(
        "absolute left-0 top-1 flex h-4 w-4 items-center justify-center rounded-full border",
        marker === "user" && "border-border bg-card",
        marker === "runtime" && "border-signal/40 bg-signal/10 text-signal",
        marker === "system" && "border-border bg-muted",
      )}>
        {marker === "user" && <User className="h-2.5 w-2.5" />}
        {marker === "runtime" && <Bot className="h-2.5 w-2.5" />}
        {marker === "system" && <OctagonPause className="h-2.5 w-2.5" />}
      </span>
      <div className="border-l border-border pl-4">{children}</div>
    </section>
  );
}
```

- [x] **Step 2: Compress tool calls to single lines**

Replace tool call + result pair layout with a single-line tool row:

```tsx
function ToolRow({ tool, result, expanded, onToggle }: { tool: ToolCall; result?: ToolResult; expanded: boolean; onToggle: () => void }) {
  return (
    <div>
      <button type="button" onClick={onToggle} className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left hover:bg-muted/50">
        {result?.status === "success" ? (
          <CheckCircle2 className="h-3.5 w-3.5 flex-none text-success" />
        ) : (
          <XCircle className="h-3.5 w-3.5 flex-none text-destructive" />
        )}
        <span className="font-mono text-xs text-muted-foreground flex-none">{tool.name}</span>
        <span className="font-mono text-xs truncate flex-1">{tool.command}</span>
        {result?.exitCode !== undefined && result.exitCode !== 0 && (
          <span className="flex-none rounded-sm bg-destructive/10 px-1 text-[10px] text-destructive">exit {result.exitCode}</span>
        )}
        <span className="flex-none text-xs text-muted-foreground">{result?.duration}</span>
        <ChevronRight className={cn("h-3.5 w-3.5 flex-none text-muted-foreground transition-transform", expanded && "rotate-90")} />
      </button>
      {expanded && result && (
        <div className="ml-5 rounded-md border border-border bg-muted/40 p-3 font-mono text-xs leading-relaxed text-muted-foreground">
          {result.output}
        </div>
      )}
    </div>
  );
}
```

- [x] **Step 3: Fold reasoning entries**

Make reasoning entries collapsible, defaulting to a single-line italic summary:

```tsx
<button type="button" onClick={onToggle} className="flex items-center gap-1.5 text-xs italic text-muted-foreground hover:text-foreground">
  <ChevronRight className={cn("h-3.5 w-3.5 transition-transform", expanded && "rotate-90")} />
  Reasoning · {summary}
</button>
{expanded && <div className="mt-1 pl-5 text-xs text-muted-foreground">{fullReasoning}</div>}
```

- [x] **Step 4: Restyle assistant message with left border**

Replace the card wrapper around assistant conclusion messages:

```tsx
// Before: <div className="rounded-lg border border-border bg-card p-4 shadow-sm">
// After:
<div className="border-l-2 border-signal/40 pl-4 text-sm leading-relaxed">
  {/* assistant content — keep code blocks with bg and copy button */}
</div>
```

- [x] **Step 5: Convert system events to centered dividers**

```tsx
<div className="flex items-center gap-3 py-1">
  <span className="h-px flex-1 bg-border" />
  <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
    <OctagonPause className="h-3 w-3" /> {eventText}
  </span>
  <span className="h-px flex-1 bg-border" />
</div>
```

- [x] **Step 6: Verify**

Run: `cd web && npx vitest run --grep "AgentTranscriptView" -v`
Expected: PASS

- [x] **Step 7: Commit**

```bash
git add web/src/components/task-transcript/AgentTranscriptView.tsx
git commit -m "refactor(workspace): Turn groups, single-line tools, collapsible results, reasoning fold, system dividers per Direction A"
```

---

## Task 4: Restructure TaskLaunchPage with hero goal, 2×2 config, accordions, and sticky summary

**Files:**
- Modify: `web/src/pages/TaskLaunchPage.tsx`
- Modify: `web/src/components/RuntimeLaunchControls.tsx`

- [x] **Step 1: Remove subtitle**

Remove the `description` prop from `ProjectPageShell`.

- [x] **Step 2: Hero goal input**

Make the goal textarea the visual hero with integrated attachment drop zone:

```tsx
<section className="rounded-xl border border-border bg-card shadow-sm">
  <div className="p-4">
    <Label htmlFor="goal" className="text-sm font-medium">你想探索什么？</Label>
    <Textarea
      id="goal"
      rows={4}
      placeholder="描述目标，例如：对 staging.example.com 做认证面枚举…"
      className="mt-2 w-full resize-none rounded-lg border border-input bg-background px-3.5 py-3 text-sm leading-relaxed outline-none placeholder:text-muted-foreground focus:border-ring"
    />
    <div className="mt-3 flex items-center justify-between rounded-lg border border-dashed border-input bg-muted/30 px-3.5 py-2.5">
      <div className="flex items-center gap-2.5 text-xs text-muted-foreground">
        <Paperclip className="h-4 w-4" />
        <span>拖拽文件到这里，或 <span className="font-medium text-foreground underline underline-offset-2">浏览</span> · 最多 25 个文件，每个 100 MB</span>
      </div>
      <span className="text-xs text-muted-foreground">投影到 Session workdir</span>
    </div>
  </div>
</section>
```

- [x] **Step 3: 2×2 common config card**

Group Runtime / Model provider / Model / Reasoning effort into a single compact card:

```tsx
<section className="rounded-xl border border-border bg-card shadow-sm">
  <div className="border-b border-border px-4 py-3">
    <span className="text-sm font-medium">Runtime 与模型</span>
  </div>
  <div className="grid grid-cols-2 gap-x-4 gap-y-4 p-4">
    {/* Runtime select */}
    {/* Model provider select */}
    {/* Model select */}
    {/* Reasoning effort segmented control */}
  </div>
</section>
```

- [x] **Step 4: Change Reasoning Effort to segmented control**

In `RuntimeLaunchControls.tsx`, replace the Select with a segmented button group:

```tsx
<div>
  <Label>Reasoning effort</Label>
  <div className="mt-1.5 flex rounded-lg border border-input p-0.5">
    {REASONING_EFFORT_VALUES.map((effort) => (
      <button
        key={effort}
        type="button"
        onClick={() => { setForm((current) => ({ ...current, reasoningEffort: effort })); setPreflight(null); }}
        className={cn(
          "flex-1 rounded-md px-2 py-1 text-xs transition-colors",
          displayReasoningEffort(form.reasoningEffort) === effort
            ? "bg-primary font-medium text-primary-foreground"
            : "text-muted-foreground hover:text-foreground",
        )}
      >
        {effort}
      </button>
    ))}
  </div>
</div>
```

- [x] **Step 5: Advanced config accordions with value summaries**

Convert Runner/network, Blackboard conclusions, Preset, Skills sections to accordions:

```tsx
function ConfigAccordion({ icon: Icon, title, summary, children, defaultOpen }: { icon: LucideIcon; title: string; summary: string; children: ReactNode; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen ?? false);
  return (
    <div className="rounded-lg border border-border">
      <button type="button" onClick={() => setOpen(!open)} className="flex w-full items-center gap-2 px-3 py-2.5 text-left">
        <ChevronRight className={cn("h-4 w-4 text-muted-foreground transition-transform", open && "rotate-90")} />
        <Icon className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium">{title}</span>
        <span className="ml-auto text-xs text-muted-foreground">{summary}</span>
      </button>
      {open && <div className="border-t border-border px-4 py-3">{children}</div>}
    </div>
  );
}
```

- [x] **Step 6: Right sticky launch summary rail**

Add a sticky right sidebar with launch summary and launch button:

```tsx
<aside className="lg:sticky lg:top-6 h-fit">
  <div className="rounded-xl border border-border bg-card shadow-sm">
    <div className="border-b border-border px-4 py-3"><span className="text-sm font-medium">Launch 摘要</span></div>
    <dl className="space-y-2.5 px-4 py-3.5 text-xs">
      <div className="flex justify-between"><dt className="text-muted-foreground">Runtime</dt><dd className="font-medium">{form.runtime}</dd></div>
      <div className="flex justify-between"><dt className="text-muted-foreground">Model</dt><dd className="font-mono">{modelDisplay}</dd></div>
      <div className="flex justify-between"><dt className="text-muted-foreground">Effort</dt><dd className="font-medium">{form.reasoningEffort}</dd></div>
      <div className="flex justify-between"><dt className="text-muted-foreground">Runner</dt><dd className="font-medium">{form.runner}</dd></div>
      <div className="flex justify-between"><dt className="text-muted-foreground">Blackboard</dt><dd className="font-medium">{blackboardConclusionMode}</dd></div>
      <div className="flex justify-between"><dt className="text-muted-foreground">Skills</dt><dd className="font-medium">{enabledSkillsPreview.length} enabled</dd></div>
    </dl>
    <div className="border-t border-border p-3.5">
      <Button onClick={launchTask} disabled={...} className="w-full">
        <Rocket className="h-4 w-4" /> Launch session
      </Button>
      <p className="mt-2 text-center text-xs text-muted-foreground">启动前会自动运行 Preflight 检查</p>
    </div>
  </div>
</aside>
```

- [x] **Step 7: Update grid layout**

Change the page body to a 2-column grid with the right rail:

```tsx
<div className="mx-auto grid max-w-[1080px] grid-cols-1 gap-6 px-8 py-6 lg:grid-cols-[1fr_300px]">
  <div className="space-y-5">{/* left column */}</div>
  <aside className="lg:sticky lg:top-6 h-fit">{/* right rail */}</aside>
</div>
```

- [x] **Step 8: Verify**

Run: `cd web && npx vitest run --grep "TaskLaunchPage|RuntimeLaunchControls" -v`
Expected: PASS

- [x] **Step 9: Commit**

```bash
git add web/src/pages/TaskLaunchPage.tsx web/src/components/RuntimeLaunchControls.tsx
git commit -m "refactor(launch): hero goal, 2x2 config, accordions, sticky summary, segmented effort per Direction A"
```

---

## Task 5: Restructure ProjectDashboardPage with scope readiness, stat cards, recent activity, current work

**Files:**
- Modify: `web/src/pages/ProjectDashboardPage.tsx`

- [x] **Step 1: Add Scope readiness checklist**

Add a warning-themed checklist section when scope is incomplete:

```tsx
{scopeIncomplete && (
  <section className="rounded-xl border border-warning/30 bg-warning/[0.06] shadow-sm">
    <div className="flex items-center justify-between border-b border-warning/20 px-4 py-3">
      <div className="flex items-center gap-2 text-sm font-medium">
        <AlertTriangle className="h-4 w-4 text-warning" /> Scope readiness
      </div>
      <span className="text-xs font-medium text-[hsl(28_90%_32%)]">{completedCount} / {totalCount} 项完成</span>
    </div>
    <div className="px-4 py-3.5">
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-warning/15">
        <div className="h-full rounded-full bg-warning" style={{ width: `${progressPercent}%` }} />
      </div>
      <ul className="mt-3 space-y-2 text-sm">
        {checklistItems.map((item) => (
          <li key={item.id} className="flex items-center gap-2.5">
            {item.done ? <CheckCircle2 className="h-4 w-4 text-success" /> : <Circle className="h-4 w-4 text-muted-foreground" />}
            <span>{item.label}</span>
            {item.action && <Button variant="outline" size="sm" className="ml-auto">{item.action}</Button>}
          </li>
        ))}
      </ul>
    </div>
  </section>
)}
```

- [x] **Step 2: Enhance stat cards with state breakdowns**

Update stat cards to show state breakdowns and be clickable:

```tsx
// Tasks card:
<Link to={`/projects/${projectId}/tasks`} className="group rounded-xl border border-border bg-card p-4 shadow-sm hover:border-ring">
  <div className="flex items-center justify-between text-xs text-muted-foreground">
    <span className="flex items-center gap-1.5"><ListChecks className="h-3.5 w-3.5" /> Tasks</span>
    <ArrowUpRight className="h-3.5 w-3.5 opacity-0 group-hover:opacity-60" />
  </div>
  <div className="mt-2 flex items-baseline gap-2">
    <span className="text-2xl font-semibold tracking-tight">{taskCount}</span>
    {runningCount > 0 && <Chip variant="success" dot className="h-[18px] text-[10px]">{runningCount} 运行中</Chip>}
  </div>
  <div className="mt-1 text-xs text-muted-foreground">最近：{latestTaskName} · {latestTaskTime}</div>
</Link>

// Findings card (zero state with dashed border):
<Link to={`/projects/${projectId}/findings`} className="group rounded-xl border border-dashed border-border bg-card/50 p-4 hover:border-ring">
  <div className="flex items-center justify-between text-xs text-muted-foreground">
    <span className="flex items-center gap-1.5"><FlaskConical className="h-3.5 w-3.5" /> Findings</span>
  </div>
  <div className="mt-2 text-2xl font-semibold tracking-tight text-muted-foreground/60">0</div>
  <div className="mt-1 text-xs text-muted-foreground">尚无 Finding — 从 Task 结论中产生</div>
</Link>
```

- [x] **Step 3: Add Recent activity section**

```tsx
<section className="col-span-3 rounded-xl border border-border bg-card shadow-sm">
  <div className="flex items-center justify-between border-b border-border px-4 py-3">
    <span className="text-sm font-medium">Recent activity</span>
    <Link to={`/projects/${projectId}/tasks`} className="text-xs text-muted-foreground hover:text-foreground">查看全部</Link>
  </div>
  <ul className="divide-y divide-border text-sm">
    {recentTasks.map((task) => (
      <li key={task.id} className="flex items-center gap-3 px-4 py-2.5">
        <span className={cn("h-1.5 w-1.5 rounded-full flex-none", statusColor(task.status))} />
        <span className="min-w-0 flex-1 truncate">{task.name} — {task.statusText}</span>
        <span className="flex-none text-xs text-muted-foreground">{formatRelativeTime(task.updatedAt)}</span>
      </li>
    ))}
  </ul>
</section>
```

- [x] **Step 4: Add Current work section**

```tsx
<section className="col-span-2 rounded-xl border border-border bg-card shadow-sm">
  <div className="border-b border-border px-4 py-3"><span className="text-sm font-medium">Current work</span></div>
  <div className="space-y-3 px-4 py-3.5 text-sm">
    <div className="flex items-center justify-between">
      <span className="flex items-center gap-2"><Compass className="h-4 w-4 text-muted-foreground" /> Exploration objectives</span>
      <span className="font-semibold">{openObjectives} 开放</span>
    </div>
    <div className="flex items-center justify-between">
      <span className="flex items-center gap-2"><FlaskConical className="h-4 w-4 text-muted-foreground" /> Attempts</span>
      <span className="font-semibold">{activeAttempts} 进行中</span>
    </div>
    <div className="flex items-center justify-between">
      <span className="flex items-center gap-2"><CheckCircle2 className="h-4 w-4 text-muted-foreground" /> Solutions</span>
      <span className="font-semibold">{verifiedSolutions} verified</span>
    </div>
    <Link to={`/projects/${projectId}/blackboard`} className="mt-1 flex h-8 items-center justify-center gap-1.5 rounded-md border border-border text-xs font-medium hover:bg-muted">
      <LayoutGrid className="h-3.5 w-3.5" /> 打开 Blackboard
    </Link>
  </div>
</section>
```

- [x] **Step 5: Convert tabs to pill style**

Update the project nav tabs to pill style with count badges:

```tsx
<nav className="flex gap-1 text-sm">
  <Link to={...} className={cn("rounded-md px-3 py-1.5", isActive ? "bg-secondary font-medium" : "text-muted-foreground hover:bg-muted")}>
    Overview
  </Link>
  <Link to={...} className={cn("rounded-md px-3 py-1.5", isActive ? "bg-secondary font-medium" : "text-muted-foreground hover:bg-muted")}>
    Tasks <span className="ml-1 rounded-sm bg-muted px-1 text-[10px]">{taskCount}</span>
  </Link>
</nav>
```

- [x] **Step 6: Verify**

Run: `cd web && npx vitest run --grep "ProjectDashboardPage" -v`
Expected: PASS

- [x] **Step 7: Commit**

```bash
git add web/src/pages/ProjectDashboardPage.tsx
git commit -m "refactor(dashboard): scope readiness, stat breakdowns, recent activity, current work, pill tabs per Direction A"
```

---

## Task 6: Restructure RuntimeProfilesPage with grouped lists, 2-card form, capability chips

**Files:**
- Modify: `web/src/pages/RuntimeProfilesPage.tsx`

- [x] **Step 1: Remove subtitle**

Remove the `description` prop from `SettingsPageHeader`.

- [x] **Step 2: Group profile list by runtime with preset/launch-resolved separation**

```tsx
// For each runtime group:
<div className="px-2 pt-1 pb-1.5"><SectionLabel>{runtimeName}</SectionLabel></div>
<div className="space-y-0.5">
  {presetProfiles.map((profile) => (
    <button key={profile.id} className={cn("block w-full rounded-md px-2 py-2 text-left hover:bg-muted/50", selected && "border border-primary/20 bg-primary/[0.03]")}>
      <div className="flex items-center gap-2">
        <span className="text-sm font-medium">{profile.name}</span>
        <Chip variant="signal" className="h-4 text-[9px]">preset</Chip>
      </div>
      <div className="mt-0.5 text-xs text-muted-foreground">{profile.modelHint}</div>
    </button>
  ))}
  {launchResolvedCount > 0 && (
    <button className="flex items-center gap-1 px-2 pt-2 pb-1 text-xs text-muted-foreground hover:text-foreground">
      <ChevronRight className="h-3 w-3" /> Launch-resolved ({launchResolvedCount})
    </button>
  )}
</div>
```

- [x] **Step 3: Top-fixed save/delete in edit header**

```tsx
<div className="flex items-center justify-between">
  <div>
    <div className="flex items-center gap-2">
      <h2 className="text-base font-semibold">{profile.name}</h2>
      <Chip variant="neutral">{profile.runtime}</Chip>
      <Chip variant="signal">{profile.kind}</Chip>
    </div>
    <p className="mt-0.5 font-mono text-xs text-muted-foreground">{profile.id}</p>
  </div>
  <div className="flex items-center gap-2">
    <Button variant="outline" size="sm"><Trash2 className="h-3.5 w-3.5" /></Button>
    <Button size="sm"><Check className="h-3.5 w-3.5" /> Save</Button>
  </div>
</div>
```

- [x] **Step 4: Merge form into 2 cards**

Merge the 6+ form sections into 2 cards:
1. "基本与模型" — Name, Provider, Model provider, Model protocol, Model override, Reasoning effort (segmented), Binary path, Default runner, Endpoint, capability chips
2. "环境与高级" — Environment variables, API key env, API key, MCP servers JSON, Sandbox image

- [x] **Step 5: Change Reasoning Effort to segmented control**

Same pattern as Task 4 Step 4 — replace Select with segmented button group.

- [x] **Step 6: Add capability chips**

Replace plain text capability list with clickable chips:

```tsx
<div className="flex flex-wrap gap-1.5">
  {capabilities.map((cap) => (
    <Chip
      key={cap.id}
      variant={cap.enabled ? "signal" : "neutral"}
      className={cap.enabled ? "bg-primary text-primary-foreground" : ""}
      onClick={() => toggleCapability(cap.id)}
    >
      {cap.name}
    </Chip>
  ))}
</div>
```

- [x] **Step 7: Add API key configured indicator**

```tsx
<div className="relative">
  <Input type="password" className="pr-16" value={apiKey} />
  {apiKeyConfigured && (
    <span className="absolute right-2 top-1/2 -translate-y-1/2 flex items-center gap-1 text-xs text-success">
      <CheckCircle2 className="h-3.5 w-3.5" /> configured
    </span>
  )}
</div>
```

- [x] **Step 8: Reduce padding**

Change the form container from `max-w-[720px] mx-auto px-8` to `px-6`.

- [x] **Step 9: Verify**

Run: `cd web && npx vitest run --grep "RuntimeProfilesPage" -v`
Expected: PASS

- [x] **Step 10: Commit**

```bash
git add web/src/pages/RuntimeProfilesPage.tsx
git commit -m "refactor(profiles): grouped lists, 2-card form, capability chips, segmented effort, API key indicator per Direction A"
```

---

## Task 7: Restructure ModelProvidersPage with key status dots, protocol chips, checkbox rows, catalog toolbar

**Files:**
- Modify: `web/src/pages/ModelProvidersPage.tsx`

- [x] **Step 1: Remove subtitle**

Remove the `description` prop from `SettingsPageHeader`.

- [x] **Step 2: Add key status dots and protocol chips to provider list**

```tsx
<div className="flex items-center gap-2">
  <span className={cn("h-1.5 w-1.5 rounded-full", hasApiKey ? "bg-success" : "bg-muted-foreground/40")} />
  <span className="text-sm font-medium">{provider.name}</span>
  {hasApiKey && <Chip variant="success" className="h-4 text-[9px]">key set</Chip>}
</div>
<div className="mt-1 font-mono text-xs text-muted-foreground truncate">{provider.baseUrl}</div>
<div className="mt-1.5 flex gap-1">
  {provider.protocols.map((p) => <Chip key={p} variant="neutral" className="h-4 text-[9px]">{p}</Chip>)}
</div>
```

- [x] **Step 3: Add header badges**

```tsx
<div className="flex items-center gap-2">
  <Chip variant="neutral" className="font-mono">{provider.apiKeyEnv}</Chip>
  <Chip variant="signal">default · {provider.defaultModel}</Chip>
  {hasApiKey && <Chip variant="success">key set</Chip>}
</div>
```

- [x] **Step 4: Change protocol cards to checkbox rows**

```tsx
<div className="space-y-2">
  {PROTOCOLS.map((protocol) => (
    <label key={protocol} className="flex items-center gap-3 cursor-pointer">
      <input type="checkbox" checked={enabledProtocols.includes(protocol)} onChange={() => toggleProtocol(protocol)} className="flex-none" />
      <span className="w-[160px] flex-none text-sm">{PROTOCOL_LABELS[protocol]}</span>
      <Input className="font-mono text-xs" value={endpointUrls[protocol]} placeholder="未配置" disabled={!enabledProtocols.includes(protocol)} />
    </label>
  ))}
</div>
```

- [x] **Step 5: Add catalog toolbar**

```tsx
<div className="border-b border-border px-4 py-3 flex items-center justify-between">
  <span className="text-sm font-medium">Catalog</span>
  <div className="flex items-center gap-1.5">
    <Button variant="outline" size="sm"><RefreshCw className="h-3 w-3" /> Refresh models</Button>
    <Button variant="outline" size="sm"><Zap className="h-3 w-3" /> Refresh capability cache</Button>
  </div>
</div>
```

- [x] **Step 6: Merge form into 2 cards**

Merge into:
1. "连接与协议" — Name, Base URL, API key, Protocols & endpoints
2. "Catalog" — Manual models, Default model, model limits

- [x] **Step 7: Separate save/delete actions**

```tsx
<div className="flex items-center justify-between">
  <Button><Check className="h-4 w-4" /> Save provider</Button>
  <Button variant="outline" className="border-destructive/30 text-destructive hover:bg-destructive/5"><Trash2 className="h-4 w-4" /> Delete</Button>
</div>
```

- [x] **Step 8: Reduce padding**

Change the form container from `max-w-[720px] mx-auto px-8` to `px-6`.

- [x] **Step 9: Verify**

Run: `cd web && npx vitest run --grep "ModelProvidersPage" -v`
Expected: PASS

- [x] **Step 10: Commit**

```bash
git add web/src/pages/ModelProvidersPage.tsx
git commit -m "refactor(providers): key status dots, protocol chips, checkbox rows, catalog toolbar, 2-card form per Direction A"
```

---

## Task 8: Restructure CredentialBindingsPage with stats header, segmented filters, table, provider links

**Files:**
- Modify: `web/src/pages/CredentialBindingsPage.tsx`

- [x] **Step 1: Remove subtitle**

Remove the `description` prop from `SettingsPageHeader`.

- [x] **Step 2: Add stats header**

```tsx
<div className="flex items-center justify-between">
  <div>
    <h2 className="text-base font-semibold">Credential library</h2>
    <p className="mt-0.5 text-xs text-muted-foreground">Bindings 在 preflight 时解析，用于 runtime profiles 和 model providers。</p>
  </div>
  <div className="text-right">
    <span className="text-xl font-semibold">{activeCount}</span>
    <span className="text-xs text-muted-foreground"> active · {totalCount} total</span>
  </div>
</div>
```

- [x] **Step 3: Convert filters to segmented controls + search**

```tsx
<div className="flex items-center gap-2">
  <div className="flex rounded-lg border border-input p-0.5">
    <button className={cn("rounded-md px-2.5 py-1 text-xs", statusFilter === "all" ? "bg-primary font-medium text-primary-foreground" : "text-muted-foreground hover:text-foreground")}>All {totalCount}</button>
    <button className={cn("rounded-md px-2.5 py-1 text-xs", statusFilter === "active" ? "bg-primary font-medium text-primary-foreground" : "text-muted-foreground hover:text-foreground")}>Active {activeCount}</button>
    <button className={cn("rounded-md px-2.5 py-1 text-xs", statusFilter === "disabled" ? "bg-primary font-medium text-primary-foreground" : "text-muted-foreground hover:text-foreground")}>Disabled {disabledCount}</button>
  </div>
  <div className="flex rounded-lg border border-input p-0.5">
    <button className={cn("rounded-md px-2.5 py-1 text-xs", sourceFilter === "all" ? "bg-secondary font-medium" : "text-muted-foreground hover:text-foreground")}>Any source</button>
    <button className={cn("rounded-md px-2.5 py-1 text-xs", sourceFilter === "env" ? "bg-secondary font-medium" : "text-muted-foreground hover:text-foreground")}>env</button>
    <button className={cn("rounded-md px-2.5 py-1 text-xs", sourceFilter === "literal" ? "bg-secondary font-medium" : "text-muted-foreground hover:text-foreground")}>literal</button>
  </div>
  <div className="flex-1" />
  <div className="flex items-center gap-2 rounded-md border border-input bg-background px-2 h-8 w-[220px]">
    <Search className="h-3.5 w-3.5 text-muted-foreground" />
    <input placeholder="Search ref, source, provider, or profile…" className="w-full bg-transparent text-xs outline-none placeholder:text-muted-foreground" />
  </div>
</div>
```

- [x] **Step 4: Convert list to table with column headers**

```tsx
<div className="overflow-hidden rounded-xl border border-border bg-card shadow-sm">
  <table className="w-full text-sm">
    <thead>
      <tr className="border-b border-border text-left text-xs uppercase tracking-wider text-muted-foreground">
        <th className="px-4 py-2.5 font-medium">Reference</th>
        <th className="px-4 py-2.5 font-medium">Source</th>
        <th className="px-4 py-2.5 font-medium">Used by</th>
        <th className="px-4 py-2.5 font-medium w-[80px]"></th>
      </tr>
    </thead>
    <tbody className="divide-y divide-border">
      {filteredBindings.map((binding) => (
        <tr key={binding.id} className="hover:bg-muted/30">
          <td className="px-4 py-3">
            <div className="font-mono font-medium">{binding.credentialRef}</div>
            <Chip variant="neutral" className="mt-1 h-4 text-[9px]">global</Chip>
            {binding.providerName && (
              <div className="mt-0.5 text-xs text-muted-foreground">Model provider · <Link to={`/model-providers?selected=${binding.providerId}`} className="text-info hover:underline">{binding.providerName}</Link></div>
            )}
          </td>
          <td className="px-4 py-3">
            <div className="font-mono text-xs">{binding.source}</div>
            <div className="text-xs text-muted-foreground">{binding.kind} ·•••••••</div>
          </td>
          <td className="px-4 py-3 text-muted-foreground">{binding.usedBy}</td>
          <td className="px-4 py-3">
            <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100">
              <Button variant="ghost" size="icon-sm"><Pencil className="h-3.5 w-3.5" /></Button>
              <Button variant="ghost" size="icon-sm" className="text-destructive"><Trash2 className="h-3.5 w-3.5" /></Button>
            </div>
          </td>
        </tr>
      ))}
    </tbody>
  </table>
</div>
```

- [x] **Step 5: Add right action panel**

```tsx
<aside className="w-[280px] flex-none border-l border-border bg-card px-5 py-6">
  <div className="rounded-xl border border-border bg-card p-4 shadow-sm">
    <h3 className="text-sm font-medium">Library actions</h3>
    <p className="mt-1.5 text-xs text-muted-foreground">引用现有密钥源而不在 UI 中存储值，除非选择 literal。</p>
    <Button className="mt-3 w-full"><Plus className="h-4 w-4" /> New binding</Button>
    <p className="mt-3 text-xs text-muted-foreground">优先在 Model providers 页面管理模型 provider API key，当密钥仅用于 LLM 认证时。</p>
  </div>
</aside>
```

- [x] **Step 6: Verify**

Run: `cd web && npx vitest run --grep "CredentialBindingsPage" -v`
Expected: PASS

- [x] **Step 7: Commit**

```bash
git add web/src/pages/CredentialBindingsPage.tsx
git commit -m "refactor(credentials): stats header, segmented filters, table, provider links, right panel per Direction A"
```

---

## Task 9: Restructure SkillsPage with profile selector card, table, toggle switches, right panel

**Files:**
- Modify: `web/src/pages/SkillsPage.tsx`

- [x] **Step 1: Remove subtitle**

Remove the `description` prop from `SettingsPageHeader`.

- [x] **Step 2: Add profile view selector card**

```tsx
<div className="flex items-center justify-between rounded-xl border border-border bg-card p-4 shadow-sm">
  <div>
    <Label className="text-xs font-medium">Runtime profile view</Label>
    <p className="mt-0.5 text-xs text-muted-foreground">Skill opt-outs 应用于此 preset 启动的每个 task。</p>
  </div>
  <Select value={profileId} onChange={(e) => setProfileId(e.target.value)} className="w-[200px]">
    {profiles.map((p) => <option key={p.id} value={p.id}>{p.name} ({p.runtime})</option>)}
  </Select>
</div>
```

- [x] **Step 3: Merge stats and filters into one row**

```tsx
<div className="flex items-center justify-between">
  <span className="text-base font-semibold">{enabledCount} <span className="text-xs font-normal text-muted-foreground">enabled · {totalCount} total</span></span>
  <div className="flex items-center gap-2">
    <div className="flex rounded-lg border border-input p-0.5">
      <button className={cn("rounded-md px-2.5 py-1 text-xs", statusFilter === "all" ? "bg-primary font-medium text-primary-foreground" : "text-muted-foreground hover:text-foreground")}>All {totalCount}</button>
      <button className={cn("rounded-md px-2.5 py-1 text-xs", statusFilter === "enabled" ? "bg-primary font-medium text-primary-foreground" : "text-muted-foreground hover:text-foreground")}>Enabled {enabledCount}</button>
      <button className={cn("rounded-md px-2.5 py-1 text-xs", statusFilter === "opted_out" ? "bg-primary font-medium text-primary-foreground" : "text-muted-foreground hover:text-foreground")}>Opted out {optedOutCount}</button>
    </div>
    <div className="flex items-center gap-2 rounded-md border border-input bg-background px-2 h-8 w-[200px]">
      <Search className="h-3.5 w-3.5 text-muted-foreground" />
      <input placeholder="Search name, id, or source…" className="w-full bg-transparent text-xs outline-none placeholder:text-muted-foreground" />
    </div>
  </div>
</div>
```

- [x] **Step 4: Convert skill list to table with toggle switches**

```tsx
<div className="overflow-hidden rounded-xl border border-border bg-card shadow-sm">
  <table className="w-full text-sm">
    <thead>
      <tr className="border-b border-border text-left text-xs uppercase tracking-wider text-muted-foreground">
        <th className="px-4 py-2.5 font-medium">Skill</th>
        <th className="px-4 py-2.5 font-medium">Description</th>
        <th className="px-4 py-2.5 font-medium w-[70px]">Source</th>
        <th className="px-4 py-2.5 font-medium w-[60px]">Enabled</th>
        <th className="px-4 py-2.5 font-medium w-[70px]"></th>
      </tr>
    </thead>
    <tbody className="divide-y divide-border">
      {filteredSkills.map((skill) => (
        <tr key={skill.id} className="hover:bg-muted/30">
          <td className="px-4 py-2.5">
            <div className="font-medium">{skill.name}</div>
            <div className="font-mono text-xs text-muted-foreground">{skill.id}</div>
          </td>
          <td className="px-4 py-2.5 text-xs text-muted-foreground">{skill.description || "—"}</td>
          <td className="px-4 py-2.5 text-xs text-muted-foreground">{skill.sourceProvenance?.kind ?? "built-in"}</td>
          <td className="px-4 py-2.5">
            <button
              type="button"
              onClick={() => toggleSkill(skill.id)}
              className={cn("relative h-5 w-9 rounded-full transition-colors", skill.enabled ? "bg-success" : "bg-muted")}
            >
              <span className={cn("absolute top-0.5 h-4 w-4 rounded-full bg-white shadow-sm transition-transform", skill.enabled ? "right-0.5" : "left-0.5")} />
            </button>
          </td>
          <td className="px-4 py-2.5">
            <div className="flex items-center gap-0.5">
              <Button variant="ghost" size="icon-sm"><Pencil className="h-3.5 w-3.5" /></Button>
              <Button variant="ghost" size="icon-sm" className="text-destructive"><Trash2 className="h-3.5 w-3.5" /></Button>
            </div>
          </td>
        </tr>
      ))}
    </tbody>
  </table>
</div>
```

- [x] **Step 5: Add right fixed upload/edit panel**

```tsx
<aside className="w-[320px] flex-none border-l border-border bg-card px-5 py-6 overflow-y-auto">
  <div className="rounded-xl border border-border bg-card p-4 shadow-sm">
    <div className="flex items-center justify-between">
      <h3 className="text-sm font-medium">Upload / edit Skill</h3>
      <Button variant="ghost" size="icon-sm"><X className="h-3.5 w-3.5" /></Button>
    </div>
    <p className="mt-1 text-xs text-muted-foreground">发布一个 canonical bundle 原子化。复用 Skill ID 会更新它。</p>
    {/* form fields */}
  </div>
  <div className="mt-4 rounded-xl border border-dashed border-border p-4">
    <div className="flex items-center gap-2 text-sm font-medium"><Download className="h-4 w-4" /> Import skill from URL or repo</div>
    <p className="mt-1.5 text-xs text-muted-foreground">结构化导入仅由 daemon 解析源本身，从不接受原始 shell 命令。</p>
  </div>
</aside>
```

- [x] **Step 6: Verify**

Run: `cd web && npx vitest run --grep "SkillsPage" -v`
Expected: PASS

- [x] **Step 7: Commit**

```bash
git add web/src/pages/SkillsPage.tsx
git commit -m "refactor(skills): profile selector card, table with toggles, right panel per Direction A"
```

---

## Task 10: Add composer status explanation banner to Workspace

**Files:**
- Modify: `web/src/components/task-transcript/AgentTranscriptView.tsx` (composer section)

- [x] **Step 1: Replace "unavailable" badge with explanation banner**

```tsx
{runtimeOffline && (
  <div className="mb-2.5 flex items-center gap-2 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-[hsl(28_90%_32%)]">
    <PlugZap className="h-3.5 w-3.5 flex-none" />
    <span>Runtime 当前离线。发送消息将恢复此 Session 并启动新的 Runtime。</span>
  </div>
)}
```

- [x] **Step 2: Merge input and model controls into focused container**

```tsx
<div className="rounded-xl border border-input bg-card shadow-sm focus-within:border-ring">
  <textarea rows={2} placeholder="Focus on admin.example.com next…" className="w-full resize-none rounded-t-xl bg-transparent px-3.5 pt-3 text-sm outline-none placeholder:text-muted-foreground" />
  <div className="flex items-center gap-1.5 px-2.5 pb-2.5">
    <Button variant="ghost" size="sm"><Paperclip className="h-3.5 w-3.5" /> Attach</Button>
    <span className="mx-1 h-4 w-px bg-border" />
    <Button variant="ghost" size="sm">{providerName} <ChevronsUpDown className="h-3 w-3 text-muted-foreground" /></Button>
    <Button variant="ghost" size="sm" className="font-medium">{modelName} <ChevronsUpDown className="h-3 w-3 text-muted-foreground" /></Button>
    <Button variant="ghost" size="sm">{effort} <ChevronsUpDown className="h-3 w-3 text-muted-foreground" /></Button>
    <span className="flex-1" />
    <span className="mr-1 text-xs text-muted-foreground">Enter 发送 · Shift+Enter 换行</span>
    <Button size="icon" className="h-8 w-8 rounded-lg"><ArrowUp className="h-4 w-4" /></Button>
  </div>
</div>
```

- [x] **Step 3: Verify**

Run: `cd web && npx vitest run --grep "AgentTranscriptView" -v`
Expected: PASS

- [x] **Step 4: Commit**

```bash
git add web/src/components/task-transcript/AgentTranscriptView.tsx
git commit -m "refactor(workspace): composer status banner and focused input container per Direction A"
```

---

## Self-Review

### Spec coverage

| Mockup Change | Task |
|---|---|
| Sidebar: search, status dots, relative time, group counts | Task 2 |
| Semantic chip system (lifecycle/activity/mode) | Task 1 (Chip component), used throughout |
| Timeline: Turn groups, single-line tools, collapsible results, reasoning fold, system dividers | Task 3 |
| Assistant message: left border instead of card | Task 3 Step 4 |
| Composer: status banner, focused container | Task 10 |
| Launch: hero goal, 2×2 config, accordions, sticky summary, segmented effort | Task 4 |
| Dashboard: scope readiness, stat breakdowns, recent activity, current work, pill tabs | Task 5 |
| Profiles: grouped lists, selected highlight, top save/delete, 2-card form, capability chips, API key indicator, segmented effort | Task 6 |
| Providers: key status dots, protocol chips, header badges, checkbox rows, catalog toolbar, 2-card form, separated actions | Task 7 |
| Credentials: stats header, segmented filters, table, provider links, right panel | Task 8 |
| Skills: profile selector card, stats+filters row, table with toggles, right panel, import note | Task 9 |
| Remove subtitles (all pages) | Tasks 4, 6, 7, 8, 9 (Step 1 in each) |

### Placeholder scan
- No "TBD", "TODO", "implement later" found
- All steps have actual code or exact instructions
- No vague "add appropriate handling" steps

### Type consistency
- `Chip` component defined in Task 1, used in Tasks 2, 5, 6, 7, 8, 9
- `SectionLabel` defined in Task 1, used in Task 6
- `REASONING_EFFORT_VALUES` and `displayReasoningEffort` already exist in `runtimeProfileForm.ts`
- `cn` already imported in all target files
- All lucide icons used are already imported or available in the codebase
