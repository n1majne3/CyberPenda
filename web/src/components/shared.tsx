import { forwardRef, type HTMLAttributes, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { ArrowLeft } from "lucide-react";
import { cn } from "@/lib/utils";
import { Card, type CardProps } from "@/components/ui";

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

export function SettingsPageHeader({
  title,
  description,
  actions,
  className,
}: {
  title: string;
  description: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "mb-6 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between",
        className,
      )}
    >
      <div className="min-w-0">
        <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
        <p className="mt-1 max-w-3xl text-sm text-muted-foreground">{description}</p>
      </div>
      {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

export function SettingsSplitLayout({
  className,
  variant = "list-detail",
  ...props
}: HTMLAttributes<HTMLDivElement> & { variant?: "list-detail" | "management" }) {
  return (
    <div
      className={cn(
        "grid min-w-0 gap-4",
        variant === "management"
          ? "lg:grid-cols-[minmax(0,1fr)_minmax(320px,380px)]"
          : "lg:grid-cols-[minmax(220px,280px)_minmax(0,1fr)]",
        className,
      )}
      {...props}
    />
  );
}

export function SettingsListPanel({ className, ...props }: CardProps) {
  return (
    <Card
      className={cn("min-w-0 overflow-hidden p-3", className)}
      {...props}
    />
  );
}

export function SettingsPanel({ className, ...props }: CardProps) {
  return (
    <Card
      className={cn("min-w-0 overflow-hidden p-4", className)}
      {...props}
    />
  );
}

export function SettingsAlert({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      role="alert"
      className={cn(
        "mb-4 rounded-lg border border-destructive/20 bg-destructive/5 px-3 py-2 text-sm text-destructive",
        className,
      )}
      {...props}
    />
  );
}
