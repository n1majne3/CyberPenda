const PROJECT_ID = "demo-project";
const TASK_ID = "demo-task";
const createdAt = "2026-08-10T08:15:00Z";
const updatedAt = "2026-08-12T09:42:00Z";

const project = {
  id: PROJECT_ID,
  name: "Acme External",
  description: "Read-only sample of a completed external security assessment.",
  kind: "pentest",
  last_activity_at: updatedAt,
  scope: {
    domains: ["app.acme.test", "api.acme.test"],
    ips: ["203.0.113.24"],
    urls: ["https://app.acme.test", "https://api.acme.test"],
    ports: ["443"],
    excluded: ["status.acme.test"],
    testing_limits: ["No denial-of-service testing"],
    notes: "Demo Scope. All targets are reserved examples.",
  },
  defaults: { runner: "sandbox" },
  created_at: createdAt,
  updated_at: updatedAt,
};

const task = {
  id: TASK_ID,
  project_id: PROJECT_ID,
  type: "pentest",
  goal: "Validate the external attack surface and confirm reportable access-control issues.",
  status: "completed",
  runner: "sandbox",
  runtime_profile_id: "demo-profile",
  run_controls: {
    blackboard_conclusion_mode: "assisted",
    sandbox_network: "restricted",
    notes: "Read-only Demo data",
  },
  scope_snapshot: project.scope,
  runtime_activity: { liveness: "offline" },
  created_at: "2026-08-11T03:10:00Z",
  updated_at: updatedAt,
};

const snapshot = {
  schema: "runtime-blackboard/v2",
  semantics: "work is active; knowledge is current; history and details are available by key",
  revision: 12,
  work: {
    objectives: {
      "objective:external-surface": {
        version: 1,
        status: "satisfied",
        objective: "Validate the external attack surface",
      },
    },
    attempts: {
      "attempt:authorization": {
        version: 2,
        status: "closed",
        summary: "Tested administrative authorization controls",
      },
    },
  },
  knowledge: {
    entities: {
      "entity:admin-api": {
        version: 1,
        status: "active",
        kind: "endpoint",
        name: "Administrative API",
        locator: "https://api.acme.test/v1/admin/users",
        scope_status: "in_scope",
      },
    },
    facts: {
      "fact:admin-api-public": {
        version: 1,
        category: "exposure",
        summary: "The administrative API is reachable from the public internet",
        confidence: "confirmed",
        scope_status: "in_scope",
      },
      "fact:version-header": {
        version: 1,
        category: "information_disclosure",
        summary: "Responses expose an exact framework version",
        confidence: "tentative",
        scope_status: "in_scope",
      },
    },
    findings: {
      "finding:admin-idor": {
        version: 2,
        status: "confirmed",
        title: "Administrative user records allow horizontal access",
        target: "https://api.acme.test/v1/admin/users/{id}",
        description: "A low-privilege user can read another tenant's administrative profile.",
        severity: "high",
        cvss_pending: false,
      },
      "finding:version-header": {
        version: 1,
        status: "unconfirmed",
        title: "Framework version exposed in response headers",
        target: "https://app.acme.test",
        severity: "low",
        cvss_pending: true,
      },
    },
    evidence: {
      "evidence:admin-response": {
        version: 1,
        status: "available",
        artifact_type: "http_exchange",
        summary: "Redacted cross-tenant administrative API response",
        media_type: "application/http",
        captured_at: "2026-08-11T05:26:00Z",
      },
      "evidence:header-capture": {
        version: 1,
        status: "available",
        artifact_type: "http_headers",
        summary: "Response headers from the public application",
        media_type: "text/plain",
        captured_at: "2026-08-11T04:02:00Z",
      },
    },
  },
  relations: [
    ["attempt:authorization", "tests", "objective:external-surface"],
    ["finding:admin-idor", "about", "entity:admin-api"],
    ["fact:admin-api-public", "supports", "finding:admin-idor"],
    ["evidence:admin-response", "evidences", "finding:admin-idor"],
    ["evidence:header-capture", "evidences", "finding:version-header"],
    ["objective:external-surface", "satisfies", "finding:admin-idor"],
  ],
};

const reportFact = {
  key: "fact:admin-api-public",
  category: "exposure",
  summary: "The administrative API is reachable from the public internet",
  confidence: "confirmed",
  scope_status: "in_scope",
};

const reportEvidence = {
  key: "evidence:admin-response",
  status: "available",
  artifact_type: "http_exchange",
  summary: "Redacted cross-tenant administrative API response",
  media_type: "application/http",
  captured_at: "2026-08-11T05:26:00Z",
};

const report = {
  schema: "pentest-report/v2",
  project: { name: project.name, description: project.description },
  confirmed_findings: [
    {
      key: "finding:admin-idor",
      title: "Administrative user records allow horizontal access",
      status: "confirmed",
      severity: "high",
      cvss_version: "3.1",
      cvss_vector: "CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:H/I:N/A:N",
      cvss_pending: false,
      target: "https://api.acme.test/v1/admin/users/{id}",
      description: "A low-privilege user can read another tenant's administrative profile.",
      proof: "A redacted request returned a record owned by another tenant.",
      impact: "An authenticated user can disclose administrative profile data.",
      recommendation: "Enforce tenant ownership checks for every record lookup.",
      supporting_facts: [reportFact],
      contradictions: [],
      evidence: [reportEvidence],
    },
  ],
  unconfirmed_findings: [
    {
      key: "finding:version-header",
      title: "Framework version exposed in response headers",
      status: "unconfirmed",
      severity: "low",
      cvss_pending: true,
      supporting_facts: [],
      contradictions: [],
      evidence: [],
    },
  ],
  confirmed_facts: [reportFact],
  tentative_facts: [
    {
      key: "fact:version-header",
      category: "information_disclosure",
      summary: "Responses expose an exact framework version",
      confidence: "tentative",
      scope_status: "in_scope",
    },
  ],
};

const reportMarkdown = `# Acme External Pentest Report

## Confirmed Findings

### High — Administrative user records allow horizontal access

A low-privilege user can read another tenant's administrative profile.

Recommendation: enforce tenant ownership checks for every record lookup.
`;

const health = {
  schema: "blackboard-health/v2",
  revision: 12,
  status: "healthy",
  attention: {
    bytes: 6840,
    estimated_tokens: 1710,
    state: "within_target",
    complete: true,
    launchable: true,
    consolidation_offered: false,
    consolidation_required: false,
  },
  anomalies: [],
  proposals: [],
};

const readRoutes: Array<[RegExp, unknown | ((path: string) => unknown)]> = [
  [/^\/api\/projects$/, { projects: [project] }],
  [/^\/api\/workspace\/navigation(?:\?.*)?$/, { revision: "demo-12", changed: true, projects: [{ ...project, tasks: [task] }] }],
  [/^\/api\/sessions(?:\?.*)?$/, { sessions: [] }],
  [new RegExp(`^/api/projects/${PROJECT_ID}$`), project],
  [new RegExp(`^/api/projects/${PROJECT_ID}/dashboard$`), {
    project_id: PROJECT_ID,
    name: project.name,
    project_kind: "pentest",
    scope: { domains: 2, ips: 1, cidrs: 0, urls: 2, ports: 1, excluded: 1, has_testing_limits: true, has_notes: true, ready: true },
    counts: { tasks: 1, facts: 2, findings: 2, evidence: 2 },
  }],
  [new RegExp(`^/api/projects/${PROJECT_ID}/tasks(?:\\?.*)?$`), { tasks: [task] }],
  [new RegExp(`^/api/projects/${PROJECT_ID}/tasks/${TASK_ID}$`), task],
  [new RegExp(`^/api/projects/${PROJECT_ID}/tasks/${TASK_ID}/finish-readiness$`), { ready_to_finish: true, blockers: [] }],
  [new RegExp(`^/api/projects/${PROJECT_ID}/tasks/${TASK_ID}/timeline(?:\\?.*)?$`), { task_id: TASK_ID, cursor: 3, has_older: false, items: [
    { seq: 1, type: "lifecycle", content: "Task started", created_at: "2026-08-11T03:10:00Z" },
    { seq: 2, type: "tool_use", tool: "http_probe", content: "Checked the administrative API", created_at: "2026-08-11T05:24:00Z" },
    { seq: 3, type: "lifecycle", content: "Task completed", created_at: updatedAt },
  ] }],
  [new RegExp(`^/api/projects/${PROJECT_ID}/tasks/${TASK_ID}/transcript(?:\\?.*)?$`), { task_id: TASK_ID, cursor: 2, has_older: false, entries: [
    { id: "demo-entry-1", seq: 1, continuation: 1, kind: "message", role: "user", text: task.goal, created_at: task.created_at },
    { id: "demo-entry-2", seq: 2, continuation: 1, kind: "message", role: "assistant", text: "Assessment complete. One confirmed high-severity Finding is ready for review.", created_at: updatedAt },
  ] }],
  [new RegExp(`^/api/v2/projects/${PROJECT_ID}/blackboard/snapshot$`), snapshot],
  [new RegExp(`^/api/v2/projects/${PROJECT_ID}/blackboard/health$`), health],
  [new RegExp(`^/api/projects/${PROJECT_ID}/reason-task-proposals$`), { proposals: [] }],
  [new RegExp(`^/api/v2/projects/${PROJECT_ID}/reports/pentest\\?format=json$`), report],
  [new RegExp(`^/api/v2/projects/${PROJECT_ID}/reports/pentest\\?format=markdown$`), { schema: "report-markdown/v2", markdown: reportMarkdown }],
  [/^\/api\/runtime-profiles$/, { profiles: [] }],
  [/^\/api\/runtime-plugins$/, { plugins: [] }],
  [/^\/api\/runtime-extensions$/, { extensions: [] }],
  [/^\/api\/runtime-extension-catalog$/, { items: [] }],
  [/^\/api\/model-providers$/, { providers: [] }],
  [/^\/api\/credential-bindings$/, { bindings: [] }],
  [/^\/api\/skills(?:\?.*)?$/, { skills: [] }],
  [/^\/api\/health$/, { status: "ok" }],
];

export function demoApiGet(path: string): unknown {
  for (const [pattern, value] of readRoutes) {
    if (pattern.test(path)) return typeof value === "function" ? value(path) : value;
  }
  throw new Error(`Demo data is not available for ${path}`);
}

export function demoApiWrite(): never {
  throw new Error("This is a read-only Demo. Run Controls and data changes are disabled.");
}
