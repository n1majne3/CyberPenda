import { forwardRef, type HTMLAttributes, type ReactNode } from "react";
import { ProjectNav } from "@/components/ProjectNav";
import { PageContainer } from "@/components/shared";
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
        className={cn("flex min-h-0 w-full min-w-0 flex-col", hideChrome && "max-w-none flex-1 p-0 sm:p-0 lg:p-0", className)}
        {...props}
      >
        {!hideChrome && (
          <div
            data-testid="project-page-shell-chrome"
            className="mb-[30px] w-full border-b border-border"
          >
            <ProjectNav />
          </div>
        )}

        <div
          className={cn(
            "flex min-h-0 w-full min-w-0 flex-1 flex-col",
            hideChrome && "max-w-none p-0 lg:p-0",
            contentClassName,
          )}
        >
          {hasHeader && (
            <div
              data-testid="project-page-shell-header"
              className="mb-7 flex w-full min-w-0 flex-row items-start justify-between gap-4 [&_h1]:text-[29px] [&_h1]:font-semibold [&_h1]:leading-[1.3] [&_h1]:tracking-[-1px] [&_h1>svg]:hidden"
            >
              <div className="min-w-0 flex-1">
                {title != null &&
                  (typeof title === "string" || typeof title === "number" ? (
                    <h1 className="text-[29px] font-semibold leading-[1.3] tracking-[-1px]">{title}</h1>
                  ) : (
                    title
                  ))}
                {description != null && (
                  <div className="mt-2 max-w-2xl text-xs leading-6 text-muted-foreground">
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
