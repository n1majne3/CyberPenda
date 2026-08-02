import { RuntimeOwnerDetailPage } from "./TaskDetailPage";

/**
 * Non-Project Sessions intentionally use the same runtime workspace as Project
 * Tasks. The shared page owns layout and interaction behavior; this route only
 * selects the Session owner adapter.
 */
export function SessionDetailPage() {
  return <RuntimeOwnerDetailPage ownerKind="session" />;
}
