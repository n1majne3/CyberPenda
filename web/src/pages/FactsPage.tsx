import { Navigate, useParams } from "react-router-dom";

/**
 * Legacy Facts bookmark compatibility: redirect to Blackboard Work filtered to
 * ProjectFact records (read contract §18.7). No frozen-table Fact index calls.
 */
export function FactsPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  return <Navigate to={`/projects/${projectId}/blackboard?node_type=project_fact`} replace />;
}
