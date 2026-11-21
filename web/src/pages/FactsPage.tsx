import { Navigate, useParams } from "react-router-dom";

export function FactsPage() {
  const { projectId = "" } = useParams<{ projectId: string }>();
  return <Navigate to={`/projects/${projectId}/blackboard/knowledge`} replace />;
}
