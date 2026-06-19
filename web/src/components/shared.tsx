import { forwardRef, type HTMLAttributes, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { ArrowLeft } from "lucide-react";
import { cn } from "@/lib/utils";

/*
 * Small presentational helpers extracted from patterns repeated across pages,
 * so the page rewrites stay focused on layout rather than re-deriving these.
 */

/** Consistent page padding + max width wrapper used by every page body. */
export const PageContainer = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ className, children, ...props }, ref) => (
    <div ref={ref} className={cn("p-6 lg:p-8", className)} {...props}>
      {children}
    </div>
  ),
);
PageContainer.displayName = "PageContainer";

/** The repeated "← Back to …" link that sits above project sub-pages. */
export function BackLink({ to, children, className }: { to: string; children: ReactNode; className?: string }) {
  return (
    <Link
      to={to}
      className={cn(
        "mb-4 inline-flex items-center gap-1 text-sm text-muted-foreground transition-colors hover:text-foreground",
        className,
      )}
    >
      <ArrowLeft className="h-4 w-4" />
      {children}
    </Link>
  );
}

/** The repeated muted "No X yet." placeholder line. */
export function EmptyState({ className, children, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return (
    <p className={cn("text-sm text-muted-foreground", className)} {...props}>
      {children}
    </p>
  );
}
