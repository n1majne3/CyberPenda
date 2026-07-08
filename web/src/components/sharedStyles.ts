import { cn } from "@/lib/utils";

export function settingsListItemClasses(selected: boolean, className?: string) {
  return cn(
    "w-full rounded-md px-2.5 py-2 text-left text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    selected
      ? "bg-accent text-foreground shadow-sm"
      : "text-muted-foreground hover:bg-accent hover:text-foreground",
    className,
  );
}
