import type { HTMLAttributes, ReactNode } from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

/*
 * Consistent sticky top bar. Pages compose a PageHeaderTitle (left) plus an
 * optional PageHeaderActions slot (right) and an optional description below.
 */
const pageHeaderVariants = cva(
  "sticky top-0 z-20 flex items-center gap-3 border-b",
  {
    variants: {
      variant: {
        default: "bg-background/80 backdrop-blur-sm",
        solid: "bg-background",
        flat: "bg-transparent",
      },
      size: {
        compact: "h-10 px-3",
        default: "h-12 px-4",
        spacious: "h-14 px-5",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export interface PageHeaderProps
  extends HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof pageHeaderVariants> {}

export function PageHeader({ className, children, variant, size, ...props }: PageHeaderProps) {
  return (
    <div
      className={cn(pageHeaderVariants({ variant, size }), className)}
      {...props}
    >
      {children}
    </div>
  );
}

const pageHeaderTitleVariants = cva("truncate font-semibold", {
  variants: {
    size: {
      sm: "text-xs",
      default: "text-sm",
      lg: "text-base",
    },
  },
  defaultVariants: { size: "default" },
});

export interface PageHeaderTitleProps
  extends VariantProps<typeof pageHeaderTitleVariants> {
  className?: string;
  children: ReactNode;
}

export function PageHeaderTitle({ className, children, size }: PageHeaderTitleProps) {
  return (
    <h2 className={cn(pageHeaderTitleVariants({ size }), className)}>
      {children}
    </h2>
  );
}

export function PageHeaderActions({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("ml-auto flex items-center gap-2", className)}>{children}</div>;
}
