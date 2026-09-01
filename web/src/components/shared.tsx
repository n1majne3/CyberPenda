import { forwardRef, type HTMLAttributes, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, ArrowLeft, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { Card, type CardProps } from "@/components/ui";

/*
 * Small presentational helpers extracted from patterns repeated across pages,
 * so the page rewrites stay focused on layout rather than re-deriving these.
 */

/** Consistent page padding + max width wrapper used by every page body. */
export const PageContainer = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ className, children, ...props }, ref) => (
    <div ref={ref} className={cn("w-full p-6 lg:p-8", className)} {...props}>
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
  eyebrow,
  className,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  eyebrow?: string;
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
        {eyebrow && (
          <p className="mb-1 font-mono text-xs uppercase tracking-[0.14em] text-muted-foreground">
            {eyebrow}
          </p>
        )}
        <h1 className="text-lg font-semibold tracking-tight">{title}</h1>
        {description && (
          <p className="mt-1 max-w-3xl text-sm text-muted-foreground">{description}</p>
        )}
      </div>
      {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

/** Uppercase tracking-wide section label used for section headers. */
export function SectionLabel({ className, children, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return (
    <p className={cn("font-mono text-xs uppercase tracking-[0.14em] text-muted-foreground", className)} {...props}>
      {children}
    </p>
  );
}

/** Fills the shell main pane on large screens so list/detail scroll independently. */
export function SettingsPageShell({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <PageContainer
      className={cn(
        "mx-auto flex w-full max-w-6xl flex-col lg:min-h-0 lg:flex-1 lg:overflow-hidden",
        className,
      )}
      {...props}
    />
  );
}

export function SettingsSplitLayout({
  className,
  variant = "list-detail",
  fill = false,
  ...props
}: HTMLAttributes<HTMLDivElement> & {
  variant?: "list-detail" | "management";
  /** Constrain to parent height and let columns scroll independently (lg+). */
  fill?: boolean;
}) {
  return (
    <div
      className={cn(
        "grid min-w-0 gap-4",
        variant === "management"
          ? "lg:grid-cols-[minmax(0,1fr)_minmax(320px,380px)]"
          : "lg:grid-cols-[minmax(220px,280px)_minmax(0,1fr)]",
        fill && "lg:min-h-0 lg:flex-1 lg:grid-rows-[minmax(0,1fr)] lg:overflow-hidden",
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

/**
 * Rich centered empty state — the gold-standard layout used by ProjectListPage.
 * Centered dashed Card with an icon avatar, title, optional description, and
 * optional CTA. Prefer this over bare muted `<p>` placeholders.
 */
export function RichEmptyState({
  icon,
  title,
  description,
  action,
  className,
}: {
  icon?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <Card
      role="status"
      className={cn(
        "items-center justify-center border-dashed py-14 text-center",
        className,
      )}
    >
      {icon && (
        <div className="flex h-10 w-10 items-center justify-center rounded-md border bg-muted text-muted-foreground">
          {icon}
        </div>
      )}
      {title && <p className="text-sm font-medium">{title}</p>}
      {description && (
        <p className="mt-1 text-sm text-muted-foreground">{description}</p>
      )}
      {action && <div className="mt-1">{action}</div>}
    </Card>
  );
}

/**
 * Rich error state with a destructive-tinted icon avatar, used for load
 * failures. Mirrors the ProjectListPage error card.
 */
export function ErrorState({
  error,
  title = "Couldn't load",
  className,
}: {
  error: string | null | undefined;
  title?: ReactNode;
  className?: string;
}) {
  if (!error) return null;
  return (
    <Card role="alert" className={cn("border-destructive/25", className)}>
      <div className="flex items-start gap-3">
        <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-destructive/20 bg-destructive/10 text-destructive">
          <AlertTriangle className="h-4 w-4" />
        </div>
        <div className="min-w-0">
          <p className="text-sm font-medium">{title}</p>
          <p className="mt-1 text-sm text-muted-foreground">{error}</p>
        </div>
      </div>
    </Card>
  );
}

/**
 * Centered loading state with a spinner. Mirrors the ProjectListPage loading
 * card, but adds a visible Loader2 spinner so it reads as "working", not frozen.
 */
export function LoadingState({
  label = "Loading",
  minHeight = "min-h-32",
  className,
}: {
  label?: string;
  minHeight?: string;
  className?: string;
}) {
  return (
    <Card
      role="status"
      aria-label={label}
      className={cn(
        "items-center justify-center gap-2 text-sm text-muted-foreground",
        minHeight,
        className,
      )}
    >
      <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden="true" />
      {label}
    </Card>
  );
}

/**
 * Shared section heading for list groupings. Consolidates the divergent h3
 * styles that were hand-rolled across Blackboard, Findings, Report, and
 * Solution pages.
 */
export function SectionHeading({
  children,
  count,
  muted = false,
  className,
}: {
  children: ReactNode;
  count?: number;
  muted?: boolean;
  className?: string;
}) {
  return (
    <h3
      className={cn(
        "text-sm font-semibold tracking-tight",
        muted && "text-muted-foreground",
        className,
      )}
    >
      {children}
      {count !== undefined && ` (${count})`}
    </h3>
  );
}
