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
      hideChrome = false,
      ...props
    },
    ref,
  ) => {
    const hasHeader = title != null || description != null || actions != null;

    return (
      <PageContainer
        ref={ref}
        className={cn("mx-auto w-full max-w-6xl", className)}
        {...props}
      >
        {!hideChrome && (
          <div
            data-testid="project-page-shell-chrome"
            className="sticky top-0 z-20 -mx-6 mb-6 w-[calc(100%+3rem)] max-w-none space-y-3 border-b border-border bg-background/90 px-6 pb-3 backdrop-blur-sm supports-[backdrop-filter]:bg-background/80 lg:-mx-8 lg:w-[calc(100%+4rem)] lg:px-8"
          >
            <BackLink to="/" className="mb-0">
              All projects
            </BackLink>
            <ProjectNav />
          </div>
        )}

        {hasHeader && (
          <div
            data-testid="project-page-shell-header"
            className="mb-6 flex w-full flex-col gap-3 sm:flex-row sm:items-start sm:justify-between"
          >
            <div className="min-w-0 flex-1">
              {title != null &&
                (typeof title === "string" || typeof title === "number" ? (
                  <h2 className="text-xl font-semibold tracking-tight">{title}</h2>
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

        <div className={cn("w-full", bodyClassName)}>{children}</div>
      </PageContainer>
    );
  },
);
ProjectPageShell.displayName = "ProjectPageShell";
