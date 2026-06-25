import {
  forwardRef,
  type ButtonHTMLAttributes,
  type HTMLAttributes,
  type InputHTMLAttributes,
  type LabelHTMLAttributes,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

/*
 * Multica-style primitives: flat ring-based elevation, dense sizing (h-8),
 * soft semantic variants, pill badges, ring-3 focus rings.
 */

// ---- Card -----------------------------------------------------------------
// Flat: a 1px hairline ring replaces drop-shadows. xl radius (14px).
export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "flex flex-col gap-4 rounded-xl bg-card px-4 py-4 text-card-foreground ring-1 ring-foreground/10",
        className,
      )}
      {...props}
    />
  );
}
export function CardHeader({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("flex flex-col", className)} {...props} />;
}
export function CardTitle({ className, ...props }: HTMLAttributes<HTMLHeadingElement>) {
  return <h3 className={cn("text-base font-medium leading-snug", className)} {...props} />;
}
export function CardDescription({ className, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return <p className={cn("text-sm text-muted-foreground", className)} {...props} />;
}
export function CardContent({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={className} {...props} />;
}
export function CardFooter({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("-mx-4 -mb-4 mt-auto flex items-center rounded-b-xl border-t bg-muted/50 px-4 py-3", className)}
      {...props}
    />
  );
}

// ---- Button ---------------------------------------------------------------
const buttonVariants = cva(
  "inline-flex items-center justify-center gap-1.5 rounded-lg border border-transparent bg-clip-padding text-sm font-medium transition-[transform,background-color,box-shadow,opacity,color,border-color] duration-200 ease-out disabled:pointer-events-none disabled:opacity-50 focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50 active:scale-[0.98] motion-reduce:active:scale-100",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground hover:bg-primary/90",
        secondary: "bg-secondary text-secondary-foreground hover:bg-secondary/80",
        // Soft destructive: translucent bg + solid text (multica treatment).
        destructive: "bg-destructive/10 text-destructive hover:bg-destructive/20",
        warning: "bg-warning text-warning-foreground hover:bg-warning/90",
        outline: "border-input bg-transparent hover:bg-accent hover:text-accent-foreground",
        ghost: "hover:bg-accent hover:text-accent-foreground",
        link: "text-primary underline-offset-4 hover:underline",
      },
      size: {
        xs: "h-6 px-2 text-xs",
        sm: "h-7 px-2.5 text-xs",
        default: "h-8 px-2.5",
        lg: "h-9 px-4",
        icon: "h-8 w-8",
        "icon-sm": "h-7 w-7",
        "icon-lg": "h-9 w-9",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);
export interface ButtonProps
  extends ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {}
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, ...props }, ref) => (
    <button ref={ref} className={cn(buttonVariants({ variant, size }), className)} {...props} />
  ),
);
Button.displayName = "Button";
export { buttonVariants };

// ---- Badge ----------------------------------------------------------------
// Pill: fully rounded (4xl), fixed height h-5. Soft semantic variants.
// Used heavily for safety states (YOLO, host runner, CVSS pending, etc.).
const badgeVariants = cva(
  "inline-flex h-5 items-center gap-1 rounded-4xl px-2 py-0.5 text-xs font-medium leading-none",
  {
    variants: {
      variant: {
        default: "bg-secondary text-secondary-foreground",
        primary: "bg-primary/10 text-primary",
        brand: "bg-brand/10 text-brand",
        success: "bg-success/10 text-success",
        warning: "bg-warning/15 text-warning",
        destructive: "bg-destructive/10 text-destructive",
        outline: "border border-border text-muted-foreground",
      },
    },
    defaultVariants: { variant: "default" },
  },
);
export interface BadgeProps
  extends HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}
export function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}

// Shared form control surface — slightly inset from card in both themes.
const formControlClasses =
  "w-full rounded-lg border border-input bg-background px-2.5 text-sm text-foreground shadow-[inset_0_1px_0_0_hsl(var(--foreground)/0.04)] placeholder:text-muted-foreground focus-visible:border-ring focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-input dark:bg-background/75";

// ---- Input ----------------------------------------------------------------
export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  ({ className, ...props }, ref) => (
    <input
      ref={ref}
      className={cn("flex h-8", formControlClasses, className)}
      {...props}
    />
  ),
);
Input.displayName = "Input";

// ---- Textarea -------------------------------------------------------------
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement>>(
  ({ className, ...props }, ref) => (
    <textarea
      ref={ref}
      className={cn("flex min-h-[80px] py-2", formControlClasses, className)}
      {...props}
    />
  ),
);
Textarea.displayName = "Textarea";

// ---- Select ---------------------------------------------------------------
// Styled native select replacing the repeated inline `className="flex h-9..."`
// selects scattered across pages. h-8 + lg radius to match the other controls.
export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(
  ({ className, ...props }, ref) => (
    <select
      ref={ref}
      className={cn("flex h-8", formControlClasses, className)}
      {...props}
    />
  ),
);
Select.displayName = "Select";

// ---- Label ----------------------------------------------------------------
export function Label({ className, ...props }: LabelHTMLAttributes<HTMLLabelElement>) {
  return (
    <label
      className={cn("text-sm font-medium leading-none text-muted-foreground", className)}
      {...props}
    />
  );
}
