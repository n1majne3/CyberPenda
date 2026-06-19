import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "@/lib/utils";

/*
 * Consistent sticky top bar — multica's `h-12 border-b px-4` page header
 * pattern. Pages compose a PageHeaderTitle (left) plus an optional
 * PageHeaderActions slot (right) and an optional description below.
 */
export function PageHeader({ className, children, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "sticky top-0 z-20 flex h-12 items-center gap-3 border-b bg-background/80 px-4 backdrop-blur-sm",
        className,
      )}
      {...props}
    >
      {children}
    </div>
  );
}

export function PageHeaderTitle({ className, children }: { className?: string; children: ReactNode }) {
  return (
    <h2 className={cn("truncate text-sm font-semibold", className)}>
      {children}
    </h2>
  );
}

export function PageHeaderActions({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("ml-auto flex items-center gap-2", className)}>{children}</div>;
}
