import { forwardRef, type HTMLAttributes, type ReactNode } from "react";
import { ProjectNav } from "@/components/ProjectNav";
import { BackLink, PageContainer } from "@/components/shared";
import { cn } from "@/lib/utils";

export type ProjectPageShellProps = {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  className?: string;
  bodyClassName?: string;
  contentClassName?: string;
  hideChrome?: boolean;
} & Omit<HTMLAttributes<HTMLDivElement>, "title">;

/**
 * Shared project page shell so section tabs sit in the same place on every route:
 * sticky "All projects" + ProjectNav, then optional page header, then body.
 */
export const ProjectPageShell = forwardRef<HTMLDivElement, ProjectPageShellProps>(
  (
    {
      title,
      description,
      actions,
      children,
      className,
      bodyClassName,
      contentClassName,
      hideChrome = false,
      ...props
    },
    ref,
  ) => {
    const hasHeader = title != null || description != null || actions != null;

    return (
      <PageContainer
        ref={ref}
        className={cn("flex w-full max-w-full min-w-0 flex-col p-0 lg:p-0", className)}
        {...props}
      >
        {!hideChrome && (
          <div
            data-testid="project-page-shell-chrome"
            className="sticky top-0 z-20 mb-6 w-full border-b border-border bg-background/90 px-6 py-3 backdrop-blur-sm supports-[backdrop-filter]:bg-background/80 lg:px-8"
          >
            <div className="mx-auto w-full max-w-6xl space-y-2">
              <BackLink to="/" className="mb-0">
                All projects
              </BackLink>
              <ProjectNav />
            </div>
          </div>
        )}

        <div
          className={cn(
            "mx-auto flex w-full max-w-6xl min-w-0 flex-1 flex-col min-h-0 px-6 pb-6 lg:px-8 lg:pb-8",
            hideChrome && "max-w-none p-0 lg:p-0",
            contentClassName,
          )}
        >
          {hasHeader && (
            <div
              data-testid="project-page-shell-header"
              className="mb-6 flex min-w-0 w-full max-w-full flex-col gap-3 sm:flex-row sm:items-start sm:justify-between"
            >
              <div className="min-w-0 flex-1">
                {title != null &&
                  (typeof title === "string" || typeof title === "number" ? (
                    <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
                  ) : (
                    title
                  ))}
                {description != null && (
                  <div className="mt-1 text-sm leading-6 text-muted-foreground">
                    {description}
                  </div>
                )}
              </div>
              {actions != null && (
                <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>
              )}
            </div>
          )}

          <div className={cn("min-w-0 w-full max-w-full", bodyClassName)}>{children}</div>
        </div>
      </PageContainer>
    );
  },
);
ProjectPageShell.displayName = "ProjectPageShell";
