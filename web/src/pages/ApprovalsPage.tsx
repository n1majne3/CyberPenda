import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowLeft, Check, ShieldAlert, X } from "lucide-react";
import { apiGet, apiPost, type Approval } from "@/lib/api";
import { ProjectNav } from "@/components/ProjectNav";
import { Badge, Button, Card } from "@/components/ui";

export function ApprovalsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  async function load() {
    if (!projectId) return;
    try {
      const d = await apiGet<{ approvals: Approval[] }>(`/api/projects/${projectId}/approvals`);
      setApprovals(d.approvals ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  /* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */
  useEffect(() => {
    // Initial load on mount/project change. load() is reused by event handlers.
    load();
  }, [projectId]);
  /* eslint-enable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */

  async function decide(id: string, decision: "approve" | "reject") {
    if (!projectId) return;
    setBusyId(id);
    try {
      await apiPost(`/api/projects/${projectId}/approvals/${id}/decide`, {
        decision,
        reviewer: "operator",
      });
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusyId(null);
    }
  }

  const pending = approvals.filter((a) => a.status === "pending");
  const decided = approvals.filter((a) => a.status !== "pending");

  return (
    <div className="p-8 max-w-4xl">
      <Link to="/" className="inline-flex items-center text-sm text-muted-foreground hover:text-foreground mb-4">
        <ArrowLeft className="h-4 w-4 mr-1" /> All projects
      </Link>
      <ProjectNav />

      <h2 className="text-xl font-semibold flex items-center gap-2 mb-4">
        <ShieldAlert className="h-5 w-5" /> Approval queue
      </h2>

      {error && <p className="text-sm text-destructive mb-4">{error}</p>}

      <section className="mb-6">
        <h3 className="text-sm font-medium text-muted-foreground uppercase tracking-wide mb-2">
          Pending ({pending.length})
        </h3>
        {pending.length === 0 && <p className="text-sm text-muted-foreground">No pending approvals.</p>}
        <div className="space-y-2">
          {pending.map((a) => (
            <ApprovalCard key={a.id} approval={a} busy={busyId === a.id} onDecide={decide} />
          ))}
        </div>
      </section>

      {decided.length > 0 && (
        <section>
          <h3 className="text-sm font-medium text-muted-foreground uppercase tracking-wide mb-2">Decided</h3>
          <div className="space-y-2">
            {decided.map((a) => (
              <ApprovalCard key={a.id} approval={a} />
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

function ApprovalCard({
  approval,
  busy,
  onDecide,
}: {
  approval: Approval;
  busy?: boolean;
  onDecide?: (id: string, decision: "approve" | "reject") => void;
}) {
  const kindLabel = approval.kind === "scope_expansion" ? "Scope expansion" : "High-risk action";
  const statusVariant =
    approval.status === "approved" ? "success" : approval.status === "rejected" ? "destructive" : "warning";

  return (
    <Card>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap gap-1 mb-1">
            <Badge variant="outline">{kindLabel}</Badge>
            <Badge variant={statusVariant}>{approval.status}</Badge>
          </div>
          <p className="font-medium">{approval.requested_action}</p>
          {approval.rationale && <p className="text-sm text-muted-foreground mt-1">{approval.rationale}</p>}
          {approval.requester && (
            <p className="text-xs text-muted-foreground mt-1">Requested by {approval.requester}</p>
          )}
          {approval.payload != null && (
            <pre className="text-xs bg-muted/50 rounded p-2 mt-2 overflow-x-auto">
              {JSON.stringify(approval.payload, null, 2)}
            </pre>
          )}
        </div>
        {approval.status === "pending" && onDecide && (
          <div className="flex gap-1 shrink-0">
            <Button size="sm" variant="secondary" disabled={busy} onClick={() => onDecide(approval.id, "approve")}>
              <Check className="h-4 w-4 mr-1" /> Approve
            </Button>
            <Button size="sm" variant="outline" disabled={busy} onClick={() => onDecide(approval.id, "reject")}>
              <X className="h-4 w-4 mr-1" /> Reject
            </Button>
          </div>
        )}
      </div>
    </Card>
  );
}