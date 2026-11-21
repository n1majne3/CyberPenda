import { cn } from "@/lib/utils";

// Dense sidebar rows: keep a visible focus target without the outer black
export const sidebarFocusClass =
  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-signal/40";

export function navItemClasses(isActive: boolean, className?: string) {
  return cn(
    "group relative inline-flex h-[37px] w-full items-center gap-2.5 rounded-md px-3 py-2 text-xs transition-colors",
    sidebarFocusClass,
    isActive
      ? "bg-card font-medium text-signal"
      : "text-muted-foreground hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground",
    className,
  );
}
