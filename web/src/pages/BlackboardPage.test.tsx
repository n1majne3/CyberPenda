import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  createMemoryRouter,
  MemoryRouter,
  Route,
  RouterProvider,
  Routes,
} from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { FORBIDDEN_ORDINARY_UI_TERMS } from "@/lib/blackboardv2";
import { BlackboardPage } from "./BlackboardPage";

const project = {
  id: "project-1",
  name: "Acme External",
  description: "External assessment",
  kind: "pentest",
  scope: {},
  defaults: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-02T00:00:00Z",
};

const ctfProject = {
  ...project,
  id: "ctf-1",
  name: "Flag CTF",
  kind: "ctf_challenge",
};

const pentestSnapshot = {
  schema: "runtime-blackboard/v2",
  semantics: "work is active; knowledge is current; history and details are available by key",
  revision: 24,
  work: {
    objectives: {
      "objective:admin": {
        version: 1,
        status: "open",
        objective: "Determine whether admin access can be bypassed",
      },
    },
    attempts: {
      "attempt:admin": {
        version: 1,
        status: "open",
        summary: "Testing the admin endpoint authorization checks",
      },
    },
  },
  knowledge: {
    entities: {
      "entity:admin": {
        version: 1,
        status: "active",
        kind: "endpoint",
        name: "Admin endpoint",
        locator: "https://example.test/admin",
        scope_status: "in_scope",
      },
    },
    facts: {
      "fact:admin": {
        version: 1,
        category: "authorization",
        summary: "The admin route responds without a privileged session",
        confidence: "tentative",
        scope_status: "in_scope",
      },
    },
    findings: {
      "finding:admin": {
        version: 1,
        status: "unconfirmed",
        title: "Admin access control bypass",
        target: "https://example.test/admin",
        severity: "critical",
        cvss_pending: false,
      },
    },
    evidence: {
      "evidence:admin": {
        version: 1,
        status: "available",
        artifact_type: "http_exchange",
        summary: "Captured unauthenticated admin response",
      },
      "evidence:missing": {
        version: 1,
        status: "missing",
        artifact_type: "screenshot",
        summary: "Expected capture was not retained",
      },
    },
  },
  relations: [
    ["attempt:admin", "about", "entity:admin"],
    ["attempt:admin", "tests", "objective:admin"],
    ["fact:admin", "about", "entity:admin"],
    ["finding:admin", "about", "entity:admin"],
    ["evidence:admin", "evidences", "finding:admin"],
    ["fact:admin", "supports", "finding:admin", "Supports the access-control concern"],
  ],
};

const emptySnapshot = {
  schema: "runtime-blackboard/v2",
  semantics: "work is active; knowledge is current; history and details are available by key",
  revision: 0,
  work: {},
  knowledge: {},
  relations: [],
};

const ctfSnapshot = {
  schema: "runtime-blackboard/v2",
  semantics: "work is active; knowledge is current; history and details are available by key",
  revision: 11,
  work: {
    objectives: {
      "objective:solve": {
        version: 1,
        status: "open",
        objective: "Recover and verify the challenge flag",
      },
    },
  },
  knowledge: {
    entities: {
      "entity:challenge": {
        version: 1,
        status: "active",
        kind: "service",
        name: "Challenge service",
        scope_status: "in_scope",
      },
    },
    solutions: {
      "solution:flag": {
        version: 1,
        status: "verified",
        kind: "flag",
        summary: "Recovered the challenge flag",
        value: "FLAG{deterministic}",
      },
    },
  },
  relations: [["solution:flag", "satisfies", "objective:solve"]],
};

const findingDetail = {
  schema: "blackboard-record/v2",
  revision: 24,
  key: "finding:admin",
  type: "finding",
  version: 2,
  record: {
    status: "unconfirmed",
    title: "Admin access control bypass",
    target: "https://example.test/admin",
    description: "The admin route may be reachable without a privileged session",
    proof: "HTTP 200 without session cookie",
    severity: "critical",
    cvss_pending: false,
  },
  relationships: [
    ["finding:admin", "about", "entity:admin"],
    ["evidence:admin", "evidences", "finding:admin"],
  ],
};

const staleDetail = {
  ...findingDetail,
  revision: 20,
};

const historyPage1 = {
  schema: "semantic-history/v2",
  revision: 24,
  key: "finding:admin",
  items: [
    {
      kind: "record",
      key: "finding:admin",
      version: 1,
      type: "finding",
      record: { status: "unconfirmed", title: "Admin access control bypass" },
    },
  ],
  next_cursor: "hist-cursor-2",
};

const historyPage2 = {
  schema: "semantic-history/v2",
  revision: 24,
  key: "finding:admin",
  items: [
    {
      kind: "record",
      key: "finding:admin",
      version: 0,
      type: "finding",
      record: { status: "unconfirmed", title: "Initial finding draft" },
    },
  ],
};

function trackFetch(handlers: {
  snapshot?: unknown;
  detail?: unknown;
  history?: (url: string) => unknown;
  project?: unknown;
  errors?: Record<string, { status: number; body: unknown }>;
}) {
  const requests: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input.toString();
    requests.push(url);

    for (const [match, err] of Object.entries(handlers.errors ?? {})) {
      if (url.includes(match)) {
        return new Response(JSON.stringify(err.body), {
          status: err.status,
          headers: { "Content-Type": "application/json" },
        });
      }
    }

    if (url.includes("/api/v2/projects/") && url.includes("/blackboard/snapshot")) {
      return json(handlers.snapshot ?? pentestSnapshot);
    }
    if (url.includes("/history")) {
      const body = handlers.history?.(url) ?? historyPage1;
      return json(body);
    }
    if (url.includes("/api/v2/projects/") && url.includes("/blackboard/records/")) {
      return json(handlers.detail ?? findingDetail);
    }
    if (url.match(/\/api\/projects\/[^/]+$/) || url.includes("/api/projects/project-1") || url.includes("/api/projects/ctf-1")) {
      if (url.includes("ctf-1")) return json(handlers.project ?? ctfProject);
      return json(handlers.project ?? project);
    }
    return json({});
  });
  vi.stubGlobal("fetch", fetchMock);
  return { fetchMock, requests };
}

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function renderBlackboard(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/projects/:projectId/blackboard" element={<BlackboardPage />} />
        <Route path="/projects/:projectId/blackboard/*" element={<BlackboardPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Blackboard UI v2 workflows", () => {
  it("loads Snapshot over /api/v2 and separates Current Work from Project Knowledge", async () => {
    const { requests } = trackFetch({});
    renderBlackboard("/projects/project-1/blackboard");

    expect(await screen.findByRole("region", { name: /Current Work/i })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: /Project Knowledge/i })).toBeInTheDocument();
    expect(screen.getByText(/Determine whether admin access can be bypassed/i)).toBeInTheDocument();
    expect(screen.getByText(/Admin access control bypass/i)).toBeInTheDocument();
    expect(screen.getByText(/revision\s+24/i)).toBeInTheDocument();

    expect(requests.some((url) => url.includes("/api/v2/projects/project-1/blackboard/snapshot"))).toBe(
      true,
    );
    expect(requests.some((url) => url.includes("/work-view"))).toBe(false);
    expect(requests.some((url) => url.includes("/graph-explorer"))).toBe(false);
    expect(requests.some((url) => url.includes("/provenance"))).toBe(false);
  });

  it("selecting a Blackboard Key loads current semantic detail without auto history", async () => {
    const { requests } = trackFetch({});
    renderBlackboard("/projects/project-1/blackboard");

    const findingLink = await screen.findByRole("link", {
      name: /Admin access control bypass/i,
    });
    expect(findingLink).toHaveAttribute(
      "href",
      "/projects/project-1/blackboard/records/finding%3Aadmin",
    );

    renderBlackboard("/projects/project-1/blackboard/records/finding%3Aadmin");
    const detail = await screen.findByRole("region", { name: /Record detail/i });
    expect(within(detail).getByText(/HTTP 200 without session cookie/i)).toBeInTheDocument();
    expect(within(detail).getAllByText("finding:admin").length).toBeGreaterThan(0);
    expect(screen.queryByRole("region", { name: /Semantic History/i })).not.toBeInTheDocument();

    const historyButton = screen.getByRole("button", { name: /Semantic History/i });
    expect(historyButton).toBeInTheDocument();

    const detailRequests = requests.filter((url) =>
      url.includes("/api/v2/projects/project-1/blackboard/records/"),
    );
    expect(detailRequests.some((url) => url.includes("/history"))).toBe(false);
  });

  it("loads Semantic History only as an explicit secondary action with pagination", async () => {
    const user = userEvent.setup();
    const { requests } = trackFetch({
      history: (url) => (url.includes("cursor=hist-cursor-2") ? historyPage2 : historyPage1),
    });
    renderBlackboard("/projects/project-1/blackboard/records/finding%3Aadmin");

    await screen.findByText(/HTTP 200 without session cookie/i);
    await user.click(screen.getByRole("button", { name: /Semantic History/i }));

    const historyRegion = await screen.findByRole("region", { name: /Semantic History/i });
    expect(within(historyRegion).getByText(/v1/i)).toBeInTheDocument();
    expect(requests.some((url) => url.includes("/history") && url.includes("limit=20"))).toBe(true);

    await user.click(screen.getByRole("button", { name: /Load more/i }));
    expect(await within(historyRegion).findByText(/Initial finding draft/i)).toBeInTheDocument();
    expect(requests.some((url) => url.includes("cursor=hist-cursor-2"))).toBe(true);
  });

  it("Graph Explorer renders keys, closed relationships, and an accessible table", async () => {
    trackFetch({});
    renderBlackboard("/projects/project-1/blackboard/explorer");

    expect(await screen.findByRole("region", { name: /Graph Explorer/i })).toBeInTheDocument();
    const recordsTable = screen.getByRole("table", { name: /Graph Explorer records/i });
    const relationsTable = screen.getByRole("table", { name: /Graph Explorer relationships/i });

    expect(within(recordsTable).getByText("finding:admin")).toBeInTheDocument();
    expect(within(recordsTable).getByText("entity:admin")).toBeInTheDocument();
    expect(within(recordsTable).getByText("objective:admin")).toBeInTheDocument();
    expect(within(relationsTable).getByText("supports")).toBeInTheDocument();
    expect(within(relationsTable).getByText("Supports the access-control concern")).toBeInTheDocument();
  });

  it("omits provenance, hashes, Fact Index, Frontier, and Recent Changes", async () => {
    trackFetch({});
    renderBlackboard("/projects/project-1/blackboard");
    await screen.findByRole("region", { name: /Current Work/i });

    const page = document.body.textContent ?? "";
    for (const term of FORBIDDEN_ORDINARY_UI_TERMS) {
      expect(page).not.toContain(term);
    }
    expect(screen.queryByText(/Provenance/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/Recent changes/i)).not.toBeInTheDocument();
  });

  it("renders CTF project-kind groups with solutions", async () => {
    trackFetch({ snapshot: ctfSnapshot, project: ctfProject });
    renderBlackboard("/projects/ctf-1/blackboard");

    expect(await screen.findByText(/Recover and verify the challenge flag/i)).toBeInTheDocument();
    expect(screen.getByText(/Recovered the challenge flag/i)).toBeInTheDocument();
    expect(screen.queryByText(/Admin access control bypass/i)).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: /Solutions/i })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: /Findings/i })).not.toBeInTheDocument();
  });

  it("renders empty states for empty Snapshot groups", async () => {
    trackFetch({ snapshot: emptySnapshot });
    renderBlackboard("/projects/project-1/blackboard");

    expect(await screen.findByText(/No open Objectives or Attempts/i)).toBeInTheDocument();
    expect(screen.getByText(/No Project Knowledge records/i)).toBeInTheDocument();
  });

  it("renders missing Evidence honestly", async () => {
    trackFetch({});
    renderBlackboard("/projects/project-1/blackboard");

    const missing = await screen.findByRole("region", { name: /Missing Evidence/i });
    expect(within(missing).getByText(/Expected capture was not retained/i)).toBeInTheDocument();
    expect(within(missing).getByText("missing")).toBeInTheDocument();
  });

  it("renders stale revision when detail lags Snapshot", async () => {
    trackFetch({ detail: staleDetail });
    renderBlackboard("/projects/project-1/blackboard/records/finding%3Aadmin");

    expect(await screen.findByRole("status", { name: /stale revision/i })).toBeInTheDocument();
    expect(screen.getByText(/detail revision 20/i)).toBeInTheDocument();
    expect(screen.getByText(/snapshot revision 24/i)).toBeInTheDocument();
  });

  it("renders semantic errors from the v2 error envelope", async () => {
    trackFetch({
      errors: {
        "/blackboard/snapshot": {
          status: 422,
          body: {
            error: {
              code: "invalid_schema",
              message: "Blackboard key is required",
              path: "key",
              retryable: false,
            },
          },
        },
      },
    });
    renderBlackboard("/projects/project-1/blackboard");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/invalid_schema/i);
    expect(alert).toHaveTextContent(/Blackboard key is required/i);
  });

  it("never requests v1 Blackboard projections from ordinary Blackboard UI", async () => {
    const { requests } = trackFetch({});
    renderBlackboard("/projects/project-1/blackboard");
    await screen.findByRole("region", { name: /Current Work/i });
    renderBlackboard("/projects/project-1/blackboard/explorer");
    await screen.findByRole("region", { name: /Graph Explorer/i });
    renderBlackboard("/projects/project-1/blackboard/records/finding%3Aadmin");
    await screen.findByText(/HTTP 200 without session cookie/i);

    const forbidden = requests.filter(
      (url) =>
        url.includes("/api/projects/") &&
        (url.includes("/blackboard/work-view") ||
          url.includes("/blackboard/entities") ||
          url.includes("/blackboard/graph-explorer") ||
          url.includes("/blackboard/health") ||
          url.includes("/provenance") ||
          (url.includes("/blackboard/records") && !url.includes("/api/v2/"))),
    );
    expect(forbidden).toEqual([]);
    expect(requests.every((url) => !url.includes("/api/projects/project-1/blackboard/"))).toBe(true);
  });

  it("keeps mobile-width content readable without page-forcing min-width tables", async () => {
    trackFetch({});
    renderBlackboard("/projects/project-1/blackboard");
    await screen.findByRole("region", { name: /Current Work/i });

    const work = screen.getByRole("region", { name: /Current Work/i });
    // Work/knowledge lists stay in the main column (no fixed side rail).
    expect(work).toHaveClass("min-w-0");
    expect(work.closest("[data-testid='blackboard-page']")).toHaveClass("min-w-0", "w-full");

    renderBlackboard("/projects/project-1/blackboard/explorer");
    const explorer = await screen.findByRole("region", { name: /Graph Explorer/i });
    expect(explorer).toHaveClass("min-w-0");

    const recordsTable = screen.getByRole("table", { name: /Graph Explorer records/i });
    const relationsTable = screen.getByRole("table", { name: /Graph Explorer relationships/i });
    // Wide tables scroll inside a containment wrapper instead of expanding the page.
    expect(recordsTable.parentElement).toHaveClass("overflow-x-auto", "min-w-0");
    expect(relationsTable.parentElement).toHaveClass("overflow-x-auto", "min-w-0");
    expect(recordsTable).toHaveClass("min-w-[36rem]");
    expect(relationsTable).toHaveClass("min-w-[36rem]");
  });

  it("CTF Graph Explorer shows a solution row that is visible and linked by key", async () => {
    trackFetch({ snapshot: ctfSnapshot, project: ctfProject });
    renderBlackboard("/projects/ctf-1/blackboard/explorer");

    const recordsTable = await screen.findByRole("table", { name: /Graph Explorer records/i });
    const solutionLink = within(recordsTable).getByRole("link", { name: "solution:flag" });
    expect(solutionLink).toHaveAttribute(
      "href",
      "/projects/ctf-1/blackboard/records/solution%3Aflag",
    );
    expect(within(recordsTable).getByText(/Recovered the challenge flag/i)).toBeInTheDocument();
    expect(within(recordsTable).getByText("solution")).toBeInTheDocument();
  });

  it("clears previous record detail and history immediately when the record key changes", async () => {
    let resolveAdmin: ((body: unknown) => void) | undefined;
    const adminDetailPromise = new Promise<unknown>((resolve) => {
      resolveAdmin = resolve;
    });

    const entityDetail = {
      schema: "blackboard-record/v2",
      revision: 24,
      key: "entity:admin",
      type: "entity",
      version: 1,
      record: { status: "active", name: "Admin endpoint", kind: "endpoint" },
      relationships: [],
    };

    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.includes("/blackboard/snapshot")) return json(pentestSnapshot);
      if (url.match(/\/api\/projects\/[^/]+$/)) return json(project);
      if (url.includes("/history")) return json(historyPage1);
      if (url.includes("/blackboard/records/") && url.includes("finding") && url.includes("admin")) {
        const body = await adminDetailPromise;
        return json(body);
      }
      if (url.includes("/blackboard/records/") && url.includes("entity") && url.includes("admin")) {
        return json(entityDetail);
      }
      return json({});
    });
    vi.stubGlobal("fetch", fetchMock);

    const router = createMemoryRouter(
      [{ path: "/projects/:projectId/blackboard/*", element: <BlackboardPage /> }],
      { initialEntries: ["/projects/project-1/blackboard/records/finding%3Aadmin"] },
    );
    render(<RouterProvider router={router} />);

    expect(await screen.findByRole("status")).toHaveTextContent(/Loading record/i);

    // Switch keys while the first detail is still in flight (same mounted RecordPanel).
    await act(async () => {
      await router.navigate("/projects/project-1/blackboard/records/entity%3Aadmin");
    });

    // Immediate clear — previous finding fields must not flash.
    expect(screen.queryByText(/HTTP 200 without session cookie/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: /Semantic History/i })).not.toBeInTheDocument();

    const detail = await screen.findByRole("region", { name: /Record detail/i });
    expect(within(detail).getByText("entity")).toBeInTheDocument();
    expect(within(detail).getAllByText("entity:admin").length).toBeGreaterThan(0);
    expect(within(detail).getAllByText("Admin endpoint").length).toBeGreaterThan(0);

    // Late admin detail must not overwrite the entity view.
    await act(async () => {
      resolveAdmin?.(findingDetail);
    });
    expect(screen.queryByText(/HTTP 200 without session cookie/i)).not.toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: /Record detail/i })).getByText("entity")).toBeInTheDocument();
  });

  it("ignores out-of-order history responses after the record key changes", async () => {
    const user = userEvent.setup();
    let resolveStaleHistory: ((body: unknown) => void) | undefined;
    const staleHistoryPromise = new Promise<unknown>((resolve) => {
      resolveStaleHistory = resolve;
    });
    const requested: string[] = [];

    const entityDetail = {
      schema: "blackboard-record/v2",
      revision: 24,
      key: "entity:admin",
      type: "entity",
      version: 1,
      record: { status: "active", name: "Unique Entity Label", kind: "endpoint" },
      relationships: [],
    };

    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      requested.push(url);
      if (url.includes("/blackboard/snapshot")) return json(pentestSnapshot);
      if (url.match(/\/api\/projects\/[^/]+$/)) return json(project);
      if (url.includes("/history") && (url.includes("finding%3A") || url.includes("finding:"))) {
        const body = await staleHistoryPromise;
        return json(body);
      }
      if (url.includes("/history")) {
        return json({
          schema: "semantic-history/v2",
          revision: 24,
          key: "entity:admin",
          items: [
            {
              kind: "record",
              key: "entity:admin",
              version: 1,
              type: "entity",
              record: { name: "Entity history item" },
            },
          ],
        });
      }
      if (url.includes("/blackboard/records/finding")) {
        return json(findingDetail);
      }
      if (url.includes("/blackboard/records/entity")) {
        return json(entityDetail);
      }
      return json({});
    });
    vi.stubGlobal("fetch", fetchMock);

    const router = createMemoryRouter(
      [{ path: "/projects/:projectId/blackboard/*", element: <BlackboardPage /> }],
      { initialEntries: ["/projects/project-1/blackboard/records/finding%3Aadmin"] },
    );
    render(<RouterProvider router={router} />);

    await screen.findByText(/HTTP 200 without session cookie/i);
    await user.click(screen.getByRole("button", { name: /Semantic History/i }));
    expect(requested.some((u) => u.includes("/history"))).toBe(true);

    // History request is in flight; switch record before it resolves.
    await act(async () => {
      await router.navigate("/projects/project-1/blackboard/records/entity%3Aadmin");
    });

    const detail = await screen.findByRole("region", { name: /Record detail/i });
    expect(within(detail).getAllByText("Unique Entity Label").length).toBeGreaterThan(0);
    expect(within(detail).getByText("entity")).toBeInTheDocument();
    expect(requested.some((u) => u.includes("/blackboard/records/entity"))).toBe(true);
    expect(screen.queryByRole("region", { name: /Semantic History/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/HTTP 200 without session cookie/i)).not.toBeInTheDocument();

    await act(async () => {
      resolveStaleHistory?.(historyPage1);
      await Promise.resolve();
    });

    // Stale finding history must not appear under the entity record.
    expect(screen.queryByRole("region", { name: /Semantic History/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/Initial finding draft/i)).not.toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: /Record detail/i })).getByText("entity")).toBeInTheDocument();
  });

  it("surfaces snapshot read failure on detail as a clear degraded state", async () => {
    trackFetch({
      errors: {
        "/blackboard/snapshot": {
          status: 503,
          body: {
            error: {
              code: "snapshot_unavailable",
              message: "Snapshot store temporarily unavailable",
              retryable: true,
            },
          },
        },
      },
    });
    renderBlackboard("/projects/project-1/blackboard/records/finding%3Aadmin");

    expect(await screen.findByText(/HTTP 200 without session cookie/i)).toBeInTheDocument();
    const degraded = await screen.findByRole("status", { name: /snapshot unavailable|degraded/i });
    expect(degraded).toHaveTextContent(/snapshot_unavailable|Snapshot store temporarily unavailable/i);
  });
});
