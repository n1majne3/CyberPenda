import { useEffect, useState } from "react";

/**
 * Tracks whether the current tab/document is visible to the user.
 *
 * Returns `false` when `document.visibilityState === "hidden"` (background tab,
 * minimized window). Used by polling consumers to suspend network traffic while
 * nobody can see the result.
 */
export function useDocumentVisibility(): boolean {
  const [isVisible, setIsVisible] = useState<boolean>(() =>
    typeof document === "undefined" ? true : document.visibilityState !== "hidden",
  );

  useEffect(() => {
    const update = () => setIsVisible(document.visibilityState !== "hidden");
    update();
    document.addEventListener("visibilitychange", update);
    return () => document.removeEventListener("visibilitychange", update);
  }, []);

  return isVisible;
}
