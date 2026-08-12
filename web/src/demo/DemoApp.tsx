import { useState, type MouseEvent, type ReactNode } from "react";
import {
  Activity,
  ArrowRight,
  CheckCircle2,
  FileCheck2,
  FileText,
  FlaskConical,
  Gauge,
  LayoutDashboard,
  LockKeyhole,
  Network,
  Play,
  Radar,
  ShieldCheck,
  TriangleAlert,
} from "lucide-react";
import { Logo } from "@/components/Logo";
import { Badge, Button, Card } from "@/components/ui";
import { cn } from "@/lib/utils";

type DemoView = "overview" | "blackboard" | "findings" | "report";

const navigation: Array<{ id: DemoView; label: string; icon: ReactNode }> = [
  { id: "overview", label: "Overview", icon: <LayoutDashboard className="size-4" /> },
  { id: "blackboard", label: "Blackboard", icon: <Network className="size-4" /> },
  { id: "findings", label: "Findings", icon: <TriangleAlert className="size-4" /> },
  { id: "report", label: "Report", icon: <FileText className="size-4" /> },
];

export function DemoApp() {
  const [view, setView] = useState<DemoView>(() => demoViewFromPath(window.location.pathname));

  const selectView = (event: MouseEvent<HTMLAnchorElement>, next: DemoView) => {
    event.preventDefault();
    window.history.replaceState({}, "", next === "overview" ? "/" : `/${next}`);
    setView(next);
  };

  return (
    <div className="min-h-svh bg-background text-foreground">
      <div className="border-b border-amber-500/20 bg-amber-500/[0.07] px-4 py-2 text-center text-xs text-amber-700 dark:text-amber-300">
        <span className="font-semibold">Public demo · Read only</span>
        <span className="mx-2 text-amber-500/50">/</span>
        This site uses authorized demonstration data. It does not start a Runtime or perform security testing.
      </div>

      <div className="flex min-h-[calc(100svh-33px)]">
        <aside className="hidden w-64 shrink-0 flex-col border-r border-border bg-sidebar md:flex">
          <div className="flex h-16 items-center gap-3 border-b border-border px-5">
            <Logo className="size-8" spin />
            <div>
              <p className="text-sm font-semibold leading-none">CyberPenda</p>
              <p className="mt-1.5 text-[11px] text-muted-foreground">Pentest Agent</p>
            </div>
          </div>
          <nav aria-label="Demo navigation" className="space-y-1 p-3">
            <p className="px-2 pb-2 pt-1 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
              Acme External Pentest
            </p>
            {navigation.map((item) => (
              <a
                key={item.id}
                href={item.id === "overview" ? "/" : `/${item.id}`}
                onClick={(event) => selectView(event, item.id)}
                aria-current={view === item.id ? "page" : undefined}
                className={cn(
                  "flex h-9 items-center gap-2.5 rounded-md px-3 text-sm transition-colors",
                  view === item.id
                    ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
                    : "text-muted-foreground hover:bg-sidebar-accent/60 hover:text-foreground",
                )}
              >
                {item.icon}
                {item.label}
              </a>
            ))}
          </nav>
          <div className="mt-auto border-t border-border p-4">
            <div className="rounded-lg border border-border bg-background/50 p-3">
              <div className="flex items-center gap-2 text-xs font-medium">
                <LockKeyhole className="size-3.5 text-muted-foreground" />
                Demo safeguards
              </div>
              <p className="mt-2 text-[11px] leading-relaxed text-muted-foreground">
                Run Controls, credentials, uploads, and Project writes are disabled.
              </p>
            </div>
          </div>
        </aside>

        <main className="min-w-0 flex-1">
          <header className="flex min-h-16 items-center justify-between gap-4 border-b border-border px-4 sm:px-6 lg:px-8">
            <div className="flex items-center gap-3">
              <Logo className="size-7 md:hidden" />
              <div>
                <p className="text-xs text-muted-foreground">Pentest Project</p>
                <h1 className="text-base font-semibold">Acme External Pentest</h1>
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Badge variant="success"><span className="size-1.5 rounded-full bg-current" />Scope active</Badge>
              <Button disabled title="Task Launch is disabled in the public demo">
                <Play className="size-3.5" />
                Launch Task
              </Button>
            </div>
          </header>

          <div className="mx-auto w-full max-w-7xl p-4 sm:p-6 lg:p-8">
            <MobileNav view={view} selectView={selectView} />
            {view === "overview" && <Overview onOpenFindings={() => setView("findings")} />}
            {view === "blackboard" && <Blackboard />}
            {view === "findings" && <Findings />}
            {view === "report" && <Report />}
          </div>
        </main>
      </div>
    </div>
  );
}

function demoViewFromPath(pathname: string): DemoView {
  const candidate = pathname.replace(/^\/+|\/+$/g, "");
  if (candidate === "blackboard" || candidate === "findings" || candidate === "report") return candidate;
  return "overview";
}

function MobileNav({
  view,
  selectView,
}: {
  view: DemoView;
  selectView: (event: MouseEvent<HTMLAnchorElement>, next: DemoView) => void;
}) {
  return (
    <nav aria-label="Demo sections" className="mb-5 flex gap-1 overflow-x-auto rounded-lg border border-border bg-card p-1 md:hidden">
      {navigation.map((item) => (
        <a
          key={item.id}
          href={item.id === "overview" ? "/" : `/${item.id}`}
          onClick={(event) => selectView(event, item.id)}
          className={cn(
            "flex shrink-0 items-center gap-1.5 rounded-md px-3 py-2 text-xs",
            view === item.id ? "bg-accent font-medium" : "text-muted-foreground",
          )}
        >
          {item.icon}{item.label}
        </a>
      ))}
    </nav>
  );
}

function PageIntro({ eyebrow, title, description }: { eyebrow: string; title: string; description: string }) {
  return (
    <div className="mb-6">
      <p className="text-xs font-medium uppercase tracking-[0.15em] text-primary">{eyebrow}</p>
      <h2 className="mt-2 text-2xl font-semibold tracking-tight sm:text-3xl">{title}</h2>
      <p className="mt-2 max-w-2xl text-sm leading-relaxed text-muted-foreground">{description}</p>
    </div>
  );
}

function Overview({ onOpenFindings }: { onOpenFindings: () => void }) {
  return (
    <>
      <PageIntro
        eyebrow="Project Dashboard"
        title="Acme External Pentest"
        description="A bounded security-testing engagement against the public Acme staging perimeter. This demonstration shows how CyberPenda keeps Scope, Tasks, Blackboard knowledge, Evidence, and Findings connected."
      />
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Metric icon={<Radar />} label="Scope assets" value="7" detail="5 domains · 2 APIs" />
        <Metric icon={<Activity />} label="Tasks" value="12" detail="10 complete · 2 stopped" />
        <Metric icon={<Network />} label="Blackboard records" value="38" detail="At revision 84" />
        <Metric icon={<FlaskConical />} label="Findings" value="3" detail="1 high · 2 medium" alert />
      </div>

      <div className="mt-6 grid gap-5 xl:grid-cols-[1.5fr_1fr]">
        <Card className="overflow-hidden p-0">
          <div className="flex items-center justify-between border-b border-border px-5 py-4">
            <div>
              <h3 className="font-medium">Recent Tasks</h3>
              <p className="mt-1 text-xs text-muted-foreground">Latest Project work and Runtime outcome</p>
            </div>
            <Badge variant="outline">Read only</Badge>
          </div>
          <div className="divide-y divide-border">
            <TaskRow title="Validate exposed administration routes" runtime="Codex · GPT-5" status="Completed" time="14 min" />
            <TaskRow title="Review session and cookie controls" runtime="Claude Code · Sonnet" status="Completed" time="9 min" />
            <TaskRow title="Test public API authorization boundaries" runtime="Pi · GPT-5" status="Stopped" time="22 min" muted />
          </div>
        </Card>

        <Card className="p-0">
          <div className="border-b border-border px-5 py-4">
            <h3 className="font-medium">Project health</h3>
            <p className="mt-1 text-xs text-muted-foreground">Current semantic and Evidence state</p>
          </div>
          <div className="space-y-4 p-5">
            <HealthRow label="Scope coverage" value="86%" tone="success" />
            <HealthRow label="Evidence availability" value="100%" tone="success" />
            <HealthRow label="Open objectives" value="2" tone="warning" />
            <HealthRow label="Unconfirmed Findings" value="1" tone="warning" />
          </div>
        </Card>
      </div>

      <Card className="mt-5 flex-row items-center justify-between gap-5 border-primary/20 bg-primary/[0.035] p-5">
        <div className="flex min-w-0 items-start gap-3">
          <ShieldCheck className="mt-0.5 size-5 shrink-0 text-primary" />
          <div>
            <h3 className="text-sm font-medium">Three reportable Findings are ready</h3>
            <p className="mt-1 text-xs leading-relaxed text-muted-foreground">Each Finding keeps its supporting Facts and Evidence references.</p>
          </div>
        </div>
        <Button variant="outline" onClick={onOpenFindings}>Review Findings <ArrowRight className="size-3.5" /></Button>
      </Card>
    </>
  );
}

function Metric({ icon, label, value, detail, alert = false }: { icon: ReactNode; label: string; value: string; detail: string; alert?: boolean }) {
  return (
    <Card className="gap-3 p-4">
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">{label}</span>
        <span className={cn("[&>svg]:size-4", alert ? "text-amber-600" : "text-muted-foreground")}>{icon}</span>
      </div>
      <div>
        <p className="text-2xl font-semibold tracking-tight">{value}</p>
        <p className="mt-1 text-xs text-muted-foreground">{detail}</p>
      </div>
    </Card>
  );
}

function TaskRow({ title, runtime, status, time, muted = false }: { title: string; runtime: string; status: string; time: string; muted?: boolean }) {
  return (
    <div className="flex items-center gap-3 px-5 py-4">
      <span className={cn("flex size-8 shrink-0 items-center justify-center rounded-full", muted ? "bg-muted text-muted-foreground" : "bg-emerald-500/10 text-emerald-600")}>
        {muted ? <Gauge className="size-4" /> : <CheckCircle2 className="size-4" />}
      </span>
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{title}</p>
        <p className="mt-1 text-xs text-muted-foreground">{runtime}</p>
      </div>
      <div className="text-right">
        <p className="text-xs font-medium">{status}</p>
        <p className="mt-1 text-[11px] text-muted-foreground">{time}</p>
      </div>
    </div>
  );
}

function HealthRow({ label, value, tone }: { label: string; value: string; tone: "success" | "warning" }) {
  return (
    <div className="flex items-center justify-between text-sm">
      <span className="text-muted-foreground">{label}</span>
      <Badge variant={tone}>{value}</Badge>
    </div>
  );
}

function Blackboard() {
  return (
    <>
      <PageIntro eyebrow="Shared Project knowledge" title="Blackboard" description="Current reusable knowledge and active exploration work at revision 84." />
      <div className="grid gap-5 lg:grid-cols-2">
        <RecordGroup title="Current Work" count="2" rows={[
          ["objective:api-boundary", "Verify object-level authorization", "Exploration Objective"],
          ["attempt:cookie-review", "Confirm cookie control impact", "Attempt"],
        ]} />
        <RecordGroup title="Project Knowledge" count="4 shown" rows={[
          ["entity:portal", "portal.staging.acme.test", "Entity"],
          ["fact:admin-route", "Administration route is public", "Confirmed Fact"],
          ["finding:cookie-secure", "Admin cookie lacks Secure", "Confirmed Finding"],
          ["evidence:cookie-header", "Authenticated response headers", "Evidence Artifact"],
        ]} />
      </div>
    </>
  );
}

function RecordGroup({ title, count, rows }: { title: string; count: string; rows: string[][] }) {
  return (
    <Card className="p-0">
      <div className="flex items-center justify-between border-b border-border px-5 py-4"><h3 className="font-medium">{title}</h3><Badge variant="outline">{count}</Badge></div>
      <div className="divide-y divide-border">
        {rows.map(([key, summary, kind]) => <div key={key} className="p-4"><div className="flex items-center justify-between gap-3"><code className="text-xs text-primary">{key}</code><Badge>{kind}</Badge></div><p className="mt-2 text-sm">{summary}</p></div>)}
      </div>
    </Card>
  );
}

function Findings() {
  return (
    <>
      <PageIntro eyebrow="Project Knowledge" title="Confirmed Findings" description="Reportable security issues supported by current Facts and Evidence." />
      <div className="space-y-4">
        <Finding severity="High" title="Admin session cookie lacks Secure attribute" keyName="finding:cookie-secure" evidence="2 Evidence Artifacts" description="The administration session cookie can be sent over an unencrypted connection when a user follows an HTTP link." />
        <Finding severity="Medium" title="Administration route discloses deployment metadata" keyName="finding:admin-metadata" evidence="1 Evidence Artifact" description="An unauthenticated route returns build identifiers and internal service names." />
        <Finding severity="Medium" title="API error responses expose internal object identifiers" keyName="finding:object-id-leak" evidence="3 Evidence Artifacts" description="Authorization failures include stable internal identifiers that help an attacker map tenant objects." />
      </div>
    </>
  );
}

function Finding({ severity, title, keyName, evidence, description }: { severity: "High" | "Medium"; title: string; keyName: string; evidence: string; description: string }) {
  return (
    <Card className="p-0">
      <div className="flex flex-col gap-4 p-5 sm:flex-row sm:items-start">
        <Badge variant={severity === "High" ? "destructive" : "warning"}>{severity}</Badge>
        <div className="min-w-0 flex-1">
          <h3 className="font-medium">{title}</h3>
          <code className="mt-1 block text-xs text-muted-foreground">{keyName}</code>
          <p className="mt-3 max-w-3xl text-sm leading-relaxed text-muted-foreground">{description}</p>
          <div className="mt-4 flex items-center gap-4 text-xs text-muted-foreground"><span className="flex items-center gap-1.5"><FileCheck2 className="size-3.5" />{evidence}</span><span>Status: confirmed</span></div>
        </div>
      </div>
    </Card>
  );
}

function Report() {
  return (
    <>
      <PageIntro eyebrow="Deliverable" title="Report preview" description="Generated from the current Blackboard. Confirmed and unconfirmed conclusions remain separate." />
      <Card className="mx-auto max-w-4xl gap-0 overflow-hidden p-0">
        <div className="border-b border-border bg-muted/30 px-6 py-4"><p className="text-xs font-medium text-muted-foreground">CYBERPENDA / PENTEST REPORT / DEMO</p></div>
        <article className="px-6 py-8 sm:px-10 sm:py-12">
          <h3 className="text-3xl font-semibold tracking-tight">Acme External Pentest</h3>
          <p className="mt-2 text-sm text-muted-foreground">Executive report · 12 August 2026</p>
          <div className="my-8 h-px bg-border" />
          <h4 className="text-lg font-semibold">Executive summary</h4>
          <p className="mt-3 text-sm leading-7 text-muted-foreground">This report contains authorized demonstration data. Testing identified three confirmed Findings across the public staging perimeter. The most important issue affects the administration session cookie.</p>
          <div className="mt-8 grid gap-3 sm:grid-cols-3"><ReportStat label="Confirmed Findings" value="3" /><ReportStat label="Confirmed Facts" value="11" /><ReportStat label="Evidence Artifacts" value="9" /></div>
          <h4 className="mt-10 text-lg font-semibold">Confirmed Findings</h4>
          <ol className="mt-4 space-y-4">
            <ReportFinding n="01" severity="HIGH" title="Admin session cookie lacks Secure attribute" />
            <ReportFinding n="02" severity="MEDIUM" title="Administration route discloses deployment metadata" />
            <ReportFinding n="03" severity="MEDIUM" title="API errors expose internal object identifiers" />
          </ol>
        </article>
      </Card>
    </>
  );
}

function ReportStat({ label, value }: { label: string; value: string }) {
  return <div className="rounded-lg border border-border p-4"><p className="text-2xl font-semibold">{value}</p><p className="mt-1 text-xs text-muted-foreground">{label}</p></div>;
}

function ReportFinding({ n, severity, title }: { n: string; severity: string; title: string }) {
  return <li className="flex items-center gap-4 rounded-lg border border-border p-4"><span className="font-mono text-xs text-muted-foreground">{n}</span><span className="min-w-0 flex-1 text-sm font-medium">{title}</span><Badge variant={severity === "HIGH" ? "destructive" : "warning"}>{severity}</Badge></li>;
}
