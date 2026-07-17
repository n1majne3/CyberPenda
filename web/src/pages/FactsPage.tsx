import { Navigate, useParams } from "react-router-dom";

/**
 * Legacy Facts bookmark compatibility: redirect to Blackboard Knowledge
 * (semantic Snapshot groups). No frozen-table Fact Index calls.
 */
export function FactsPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  return <Navigate to={`/projects/${projectId}/blackboard/knowledge`} replace />;
}
