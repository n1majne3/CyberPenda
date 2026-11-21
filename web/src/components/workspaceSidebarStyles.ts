import { cn } from "@/lib/utils";

// Dense sidebar rows: keep a visible focus target without the outer black
export const sidebarFocusClass =
  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-signal/40";

export function navItemClasses(isActive: boolean, className?: string) {
  return cn(
    "group relative inline-flex h-9 w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors",
    sidebarFocusClass,
    isActive
      ? "bg-signal/5 font-semibold text-sidebar-accent-foreground"
      : "text-muted-foreground hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground",
    className,
  );
}
