import { fgsGraph, fgsHistory, fgsReport, fgsStatus } from "./fgsFixture";

const PROJECT_ID = "demo-project";
const TASK_ID = "demo-task";
const createdAt = "2026-09-28T08:17:38Z";
const updatedAt = "2026-09-28T09:36:56Z";

const project = {
  id: PROJECT_ID,
  name: "VulnCastle",
  description: "Read-only sample of a completed multi-host penetration test against an intentionally vulnerable lab.",
  kind: "pentest",
  blackboard_protocol: "fgs",
  last_activity_at: updatedAt,
  scope: {
    ips: ["172.17.0.1"],
    cidrs: ["10.10.0.0/24", "172.20.1.0/24"],
    urls: ["http://172.17.0.1:8080", "http://172.17.0.1:8025"],
    ports: ["8080", "8025", "2222"],
    notes: "Demo Scope. VulnCastle lab targets are reserved examples; entry SSH credentials are redacted.",
  },
  defaults: { runner: "sandbox" },
  created_at: createdAt,
  updated_at: updatedAt,
};

const task = {
  id: TASK_ID,
  project_id: PROJECT_ID,
  type: "pentest",
  blackboard_protocol: "fgs",
  goal: "Penetration test of the VulnCastle training lab. Enumerate the entry points, exploit vulnerabilities, pivot into the DMZ and internal networks, escalate privileges, and record all findings with evidence. All listed targets are authorized for testing.",
  status: "completed",
  runner: "sandbox",
  runtime_profile_id: "demo-profile",
  run_controls: {
    blackboard_mode: "working_graph",
    sandbox_network: "restricted",
    notes: "Read-only Demo data",
  },
  scope_snapshot: project.scope,
  runtime_activity: { liveness: "offline" },
  created_at: createdAt,
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

const runtimeProfile = {
  id: "demo-profile",
  name: "Codex Sandbox",
  provider: "codex",
  fields: {
    model_provider_id: "demo-provider",
    model: "gpt-5.2-codex",
    reasoning_effort: "medium",
    default_runner: "sandbox",
    sandbox_image: "cyberpenda/sandbox:2026-08",
    custom_args: [],
  },
  created_at: createdAt,
  updated_at: updatedAt,
};

const builtinSource = { kind: "builtin" };

const skills = [
  {
    id: "tooling-nmap",
    name: "nmap",
    description: "Canonical Nmap CLI syntax, two-pass scanning workflow, and sandbox-safe bounded scan patterns.",
    source_provenance: builtinSource,
    enabled: true,
    created_at: createdAt,
    updated_at: updatedAt,
  },
  {
    id: "tooling-httpx",
    name: "httpx",
    description: "httpx probing patterns for fast host, status, and technology discovery on in-scope targets.",
    source_provenance: builtinSource,
    enabled: true,
    created_at: createdAt,
    updated_at: updatedAt,
  },
  {
    id: "tooling-ffuf",
    name: "ffuf",
    description: "Fuzzing workflows for content and parameter discovery against authenticated endpoints.",
    source_provenance: builtinSource,
    enabled: true,
    created_at: createdAt,
    updated_at: updatedAt,
  },
  {
    id: "tooling-nuclei",
    name: "nuclei",
    description: "Template-driven vulnerability scanning with curated, sandbox-safe checksets.",
    source_provenance: builtinSource,
    enabled: false,
    globally_opted_out: true,
    created_at: createdAt,
    updated_at: updatedAt,
  },
  {
    id: "tooling-sqlmap",
    name: "sqlmap",
    description: "SQL injection detection and data extraction playbooks for authenticated APIs.",
    source_provenance: builtinSource,
    enabled: false,
    profile_opted_out: true,
    created_at: createdAt,
    updated_at: updatedAt,
  },
  {
    id: "ctf-orchestrator",
    name: "ctf-orchestrator",
    description: "Orchestrate a timed multi-target offensive/CTF session with a Decide/Execute + knowledge-graph architecture.",
    source_provenance: builtinSource,
    enabled: true,
    created_at: createdAt,
    updated_at: updatedAt,
  },
  {
    id: "pivot-runbook",
    name: "Multi-host pivot Runbook",
    description: "Operator-authored runbook capturing the SOCKS pivot and cross-host credential-reuse test procedure.",
    enabled: true,
    created_at: createdAt,
    updated_at: updatedAt,
  },
];

const demoAssetBase = {
  project_id: PROJECT_ID,
  first_seen_at: createdAt,
  last_seen_at: updatedAt,
  created_at: createdAt,
  updated_at: updatedAt,
  goal_count: 0,
  step_count: 0,
  fact_count: 0,
  anchors: [],
};

const demoAssets = [
  {
    ...demoAssetBase,
    id: "demo-asset-ip",
    type: "ip",
    value: "172.17.0.1",
    normalized_value: "172.17.0.1",
    host: "172.17.0.1",
    scope_status: "in_scope",
    covered: false,
  },
  {
    ...demoAssetBase,
    id: "demo-asset-service",
    type: "service",
    value: "http://172.17.0.1:8080",
    normalized_value: "http://172.17.0.1:8080",
    scheme: "http",
    host: "172.17.0.1",
    port: 8080,
    parent_id: "demo-asset-ip",
    scope_status: "in_scope",
    covered: false,
  },
  {
    ...demoAssetBase,
    id: "demo-asset-endpoint",
    type: "endpoint",
    value: "http://172.17.0.1:8080/login",
    normalized_value: "http://172.17.0.1:8080/login",
    scheme: "http",
    host: "172.17.0.1",
    port: 8080,
    path: "/login",
    parent_id: "demo-asset-service",
    scope_status: "in_scope",
    covered: true,
    fact_count: 1,
    anchors: [{ project_id: PROJECT_ID, asset_id: "demo-asset-endpoint", node_key: "fact:login-auth-bypass", node_type: "fact", actor_id: "runtime:demo", created_at: updatedAt }],
  },
];

const demoAssetCoverage = {
  schema: "project-asset-coverage/v1",
  project_id: PROJECT_ID,
  total_assets: 3,
  in_scope_assets: 3,
  covered_in_scope_assets: 1,
  coverage_percent: 33,
  out_of_scope_assets: 0,
  excluded_assets: 0,
  uncovered_in_scope_asset_ids: ["demo-asset-ip", "demo-asset-service"],
};

const readRoutes: Array<[RegExp, unknown | ((path: string) => unknown)]> = [
  [/^\/api\/projects$/, { projects: [project] }],
  [/^\/api\/workspace\/navigation(?:\?.*)?$/, { revision: "demo-12", changed: true, projects: [{ ...project, tasks: [task] }] }],
  [/^\/api\/sessions(?:\?.*)?$/, { sessions: [] }],
  [new RegExp(`^/api/projects/${PROJECT_ID}$`), project],
  [new RegExp(`^/api/projects/${PROJECT_ID}/scope-expansions$`), { expansions: [] }],
  [new RegExp(`^/api/projects/${PROJECT_ID}/assets$`), { assets: demoAssets }],
  [new RegExp(`^/api/projects/${PROJECT_ID}/assets/coverage$`), demoAssetCoverage],
  [new RegExp(`^/api/projects/${PROJECT_ID}/dashboard$`), {
    project_id: PROJECT_ID,
    name: project.name,
    project_kind: "pentest",
    scope: { domains: 0, ips: 1, cidrs: 2, urls: 2, ports: 3, excluded: 0, has_testing_limits: false, has_notes: true, ready: true },
    counts: { tasks: 1, goals: 5, steps: 11, facts: 15, assets: 3 },
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
  [new RegExp(`^/api/v2/projects/${PROJECT_ID}/fgs/status$`), fgsStatus],
  [new RegExp(`^/api/v2/projects/${PROJECT_ID}/fgs/report$`), fgsReport],
  [new RegExp(`^/api/v2/projects/${PROJECT_ID}/fgs/nodes/(.+)/history(?:\\?.*)?$`), (path: string) => {
    const match = path.match(new RegExp(`/fgs/nodes/(.+)/history`));
    return fgsHistory(match ? decodeURIComponent(match[1]) : "");
  }],
  [new RegExp(`^/api/v2/projects/${PROJECT_ID}/fgs(?:\\?.*)?$`), fgsGraph],
  [new RegExp(`^/api/v2/projects/${PROJECT_ID}/reports/pentest\\?format=json$`), report],
  [new RegExp(`^/api/v2/projects/${PROJECT_ID}/reports/pentest\\?format=markdown$`), { schema: "report-markdown/v2", markdown: reportMarkdown }],
  [/^\/api\/runtime-profiles$/, { profiles: [runtimeProfile] }],
  [new RegExp(`^/api/runtime-profiles/${runtimeProfile.id}/model-provider-migration-preview$`), { eligible: false, reason: "Profile already uses a Model Provider.", matches: [], api_key_sources: [] }],
  [new RegExp(`^/api/runtime-profiles/${runtimeProfile.id}/merged-config-preview$`), {
    provider: runtimeProfile.provider,
    merged: {
      model_provider_id: "demo-provider",
      model: "gpt-5.2-codex",
      reasoning_effort: "medium",
      default_runner: "sandbox",
      sandbox_image: "cyberpenda/sandbox:2026-08",
    },
  }],
  [new RegExp(`^/api/runtime-profiles/${runtimeProfile.id}/projected-config$`), {
    provider: runtimeProfile.provider,
    format: "markdown",
    text: "# Codex launch environment (redacted demo)\n\n- model_provider_id: demo-provider\n- model: gpt-5.2-codex\n- default_runner: sandbox\n",
  }],
  [/^\/api\/runtime-plugins$/, { plugins: [] }],
  [/^\/api\/runtime-extensions$/, { extensions: [] }],
  [/^\/api\/runtime-extension-catalog$/, { items: [] }],
  [/^\/api\/model-providers$/, { providers: [] }],
  [/^\/api\/credential-bindings$/, { bindings: [] }],
  [/^\/api\/skills(?:\?.*)?$/, { skills }],
  [/^\/api\/skills\/([^/?]+)$/, (path: string) => {
    const id = decodeURIComponent(path.split("/").pop() ?? "");
    const found = skills.find((skill) => skill.id === id);
    if (!found) throw new Error(`Demo data is not available for ${path}`);
    return { ...found, files: { "SKILL.md": `# ${found.name}\n\nRead-only demo instruction body for ${found.name}.\n` } };
  }],
  [/^\/health$/, {
    version: "demo",
    database: { status: "ok" },
    runner: { container_cli: "docker", engine_kind: "docker", engine_name: "Docker" },
  }],
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
