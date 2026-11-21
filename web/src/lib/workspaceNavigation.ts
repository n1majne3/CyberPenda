export const WORKSPACE_NAVIGATION_CHANGED_EVENT = "cyberpenda:workspace-navigation-changed";

export function notifyWorkspaceNavigationChanged() {
  window.dispatchEvent(new Event(WORKSPACE_NAVIGATION_CHANGED_EVENT));
}
