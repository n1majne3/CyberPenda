import type { HTMLAttributes, InputHTMLAttributes, ReactNode } from "react";
import { Search } from "lucide-react";
import { Input, type InputProps } from "@/components/ui";
import { SettingsPanel } from "@/components/shared";
import { cn } from "@/lib/utils";

/** Left/right column wrapper for fill-height settings list-detail layouts. */
export function SettingsListColumn({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "flex min-w-0 flex-col gap-3 lg:min-h-0 lg:overflow-hidden",
        className,
      )}
      {...props}
    />
  );
}

/** Scrollable list body panel used under a toolbar in fill layouts. */
export function SettingsScrollPanel({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <SettingsPanel
      className={cn(
        "gap-0 p-2 lg:min-h-0 lg:flex-1 lg:overflow-y-auto lg:overscroll-contain",
        className,
      )}
      {...props}
    />
  );
}

export type SettingsSearchFieldProps = Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "size" | "onChange" | "value"
> & {
  value: string;
  onChange: (value: string) => void;
  size?: InputProps["size"];
  inputClassName?: string;
};

/** Search input with leading icon — shared library discovery control. */
export function SettingsSearchField({
  value,
  onChange,
  className,
  inputClassName,
  size = "default",
  ...props
}: SettingsSearchFieldProps) {
  return (
    <div className={cn("relative min-w-0 flex-1", className)}>
      <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
      <Input
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className={cn("pl-8", inputClassName)}
        size={size}
        autoComplete="off"
        spellCheck={false}
        {...props}
      />
    </div>
  );
}

export type SettingsFilterOption<T extends string = string> = {
  id: T;
  label: string;
  count?: number;
};

export type SettingsSegmentedFilterProps<T extends string = string> = {
  value: T;
  onChange: (value: T) => void;
  options: readonly SettingsFilterOption<T>[];
  "aria-label": string;
  className?: string;
};

/** Segmented control for All / Active / Disabled style filters. */
export function SettingsSegmentedFilter<T extends string>({
  value,
  onChange,
  options,
  "aria-label": ariaLabel,
  className,
}: SettingsSegmentedFilterProps<T>) {
  return (
    <div
      className={cn(
        "flex shrink-0 rounded-md border border-border bg-muted/40 p-0.5",
        className,
      )}
      role="group"
      aria-label={ariaLabel}
    >
      {options.map((option) => (
        <button
          key={option.id}
          type="button"
          onClick={() => onChange(option.id)}
          className={cn(
            "rounded-[5px] px-2.5 py-1 text-xs font-medium transition-colors",
            value === option.id
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground",
          )}
          aria-pressed={value === option.id}
        >
          {option.label}
          {option.count != null && (
            <span className="ml-1 tabular-nums opacity-60"> {option.count}</span>
          )}
        </button>
      ))}
    </div>
  );
}

export type SettingsDetailPaneProps = {
  header?: ReactNode;
  footer?: ReactNode;
  children?: ReactNode;
  className?: string;
  bodyClassName?: string;
  empty?: boolean;
  emptyContent?: ReactNode;
  "data-testid"?: string;
};

/**
 * Detail panel with optional sticky header/footer and independent scroll body
 * for fill-height settings layouts.
 */
export function SettingsDetailPane({
  header,
  footer,
  children,
  className,
  bodyClassName,
  empty,
  emptyContent,
  "data-testid": dataTestId,
}: SettingsDetailPaneProps) {
  return (
    <SettingsPanel
      data-testid={dataTestId}
      className={cn(
        "flex min-w-0 flex-col p-0 lg:min-h-0 lg:overflow-hidden",
        className,
      )}
    >
      {empty ? (
        <div className="flex h-full min-h-[12rem] items-center justify-center px-4 text-sm text-muted-foreground">
          {emptyContent}
        </div>
      ) : (
        <>
          {header != null && (
            <div className="border-b border-border px-4 py-3 lg:shrink-0">
              {header}
            </div>
          )}
          <div
            className={cn(
              "space-y-4 px-4 py-4 lg:min-h-0 lg:flex-1 lg:overflow-y-auto lg:overscroll-contain",
              bodyClassName,
            )}
          >
            {children}
          </div>
          {footer != null && (
            <div className="flex flex-wrap items-center gap-2 border-t border-border bg-card/95 px-4 py-3 backdrop-blur supports-[backdrop-filter]:bg-card/80 lg:shrink-0">
              {footer}
            </div>
          )}
        </>
      )}
    </SettingsPanel>
  );
}
