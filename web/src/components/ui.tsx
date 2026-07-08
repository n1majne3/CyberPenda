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

// ---- Card -----------------------------------------------------------------
const cardVariants = cva(
  "flex flex-col rounded-lg border border-border bg-card text-card-foreground shadow-sm",
  {
    variants: {
      variant: {
        default: "",
        flat: "shadow-none",
        elevated: "shadow-md",
      },
      size: {
        compact: "gap-3 p-3",
        default: "gap-4 p-4",
        spacious: "gap-5 p-5",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);
export interface CardProps
  extends HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof cardVariants> {}
export function Card({ className, variant, size, ...props }: CardProps) {
  return (
    <div
      className={cn(cardVariants({ variant, size }), className)}
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
      className={cn("mt-auto flex items-center border-t bg-muted/40 px-4 py-3", className)}
      {...props}
    />
  );
}

// ---- Button ---------------------------------------------------------------
const buttonVariants = cva(
  "inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-md border border-transparent text-sm font-medium transition-colors duration-150 disabled:pointer-events-none disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground shadow-sm hover:bg-primary/90",
        secondary: "border-border bg-secondary text-secondary-foreground shadow-sm hover:bg-secondary/80",
        destructive: "bg-destructive text-destructive-foreground shadow-sm hover:bg-destructive/90",
        warning: "bg-warning text-warning-foreground shadow-sm hover:bg-warning/90",
        outline: "border-border bg-background shadow-sm hover:bg-accent hover:text-accent-foreground",
        ghost: "hover:bg-accent hover:text-accent-foreground",
        link: "text-primary underline-offset-4 hover:underline",
      },
      size: {
        xs: "h-6 px-2 text-xs",
        sm: "h-7 px-2.5 text-xs",
        default: "h-8 px-3",
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
const badgeVariants = cva(
  "inline-flex items-center gap-1 rounded-md border text-xs font-medium leading-none",
  {
    variants: {
      variant: {
        default: "border-transparent bg-secondary text-secondary-foreground",
        primary: "border-primary/15 bg-primary/5 text-primary",
        success: "border-success/20 bg-success/10 text-success",
        warning: "border-warning/25 bg-warning/10 text-warning",
        destructive: "border-destructive/20 bg-destructive/10 text-destructive",
        outline: "border-border bg-background text-muted-foreground",
      },
      size: {
        sm: "h-5 px-1.5 py-0",
        default: "h-6 px-2 py-0.5",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);
export interface BadgeProps
  extends HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}
export function Badge({ className, variant, size, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant, size }), className)} {...props} />;
}

const controlBaseClasses =
  "w-full rounded-md border bg-background text-sm text-foreground shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:border-ring focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-50";
const controlToneVariants = {
  default: "border-input",
  invalid: "border-destructive focus-visible:border-destructive focus-visible:ring-destructive/30",
} as const;

const inputVariants = cva(cn("flex", controlBaseClasses), {
  variants: {
    variant: controlToneVariants,
    size: {
      sm: "h-7 px-2 text-xs",
      default: "h-8 px-3",
      lg: "h-9 px-3",
    },
  },
  defaultVariants: { variant: "default", size: "default" },
});

// ---- Input ----------------------------------------------------------------
export interface InputProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, "size">,
    VariantProps<typeof inputVariants> {}
export const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ className, variant, size, ...props }, ref) => (
    <input
      ref={ref}
      className={cn(inputVariants({ variant, size }), className)}
      {...props}
    />
  ),
);
Input.displayName = "Input";

// ---- Textarea -------------------------------------------------------------
const textareaVariants = cva(cn("flex px-3 py-2", controlBaseClasses), {
  variants: {
    variant: controlToneVariants,
    size: {
      sm: "min-h-[64px]",
      default: "min-h-[80px]",
      lg: "min-h-[120px]",
    },
  },
  defaultVariants: { variant: "default", size: "default" },
});
export interface TextareaProps
  extends Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "size">,
    VariantProps<typeof textareaVariants> {}
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(
  ({ className, variant, size, ...props }, ref) => (
    <textarea
      ref={ref}
      className={cn(textareaVariants({ variant, size }), className)}
      {...props}
    />
  ),
);
Textarea.displayName = "Textarea";

// ---- Select ---------------------------------------------------------------
export interface SelectProps
  extends Omit<SelectHTMLAttributes<HTMLSelectElement>, "size">,
    VariantProps<typeof inputVariants> {}
export const Select = forwardRef<HTMLSelectElement, SelectProps>(
  ({ className, variant, size, ...props }, ref) => (
    <select
      ref={ref}
      className={cn(inputVariants({ variant, size }), className)}
      {...props}
    />
  ),
);
Select.displayName = "Select";

// ---- Label ----------------------------------------------------------------
const labelVariants = cva("font-medium leading-none", {
  variants: {
    variant: {
      default: "text-foreground",
      muted: "text-muted-foreground",
    },
    size: {
      sm: "text-xs",
      default: "text-sm",
    },
  },
  defaultVariants: { variant: "default", size: "default" },
});
export interface LabelProps
  extends Omit<LabelHTMLAttributes<HTMLLabelElement>, "size">,
    VariantProps<typeof labelVariants> {}
export function Label({ className, variant, size, ...props }: LabelProps) {
  return (
    <label
      className={cn(labelVariants({ variant, size }), className)}
      {...props}
    />
  );
}
