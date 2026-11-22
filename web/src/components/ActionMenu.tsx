import { useRef, type ReactNode } from "react";
import { ChevronDown } from "lucide-react";
import { buttonVariants } from "@/components/buttonVariants";

export function ActionMenu({ label = "More actions", children }: { label?: string; children: ReactNode }) {
  const ref = useRef<HTMLDetailsElement>(null);

  function close() {
    if (ref.current) ref.current.open = false;
  }

  return (
    <details
      ref={ref}
      className="relative shrink-0"
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) close();
      }}
      onKeyDown={(event) => {
        if (event.key !== "Escape") return;
        event.preventDefault();
        close();
        ref.current?.querySelector("summary")?.focus();
      }}
    >
      <summary className={`${buttonVariants({ variant: "outline", size: "sm" })} cursor-pointer list-none [&::-webkit-details-marker]:hidden`}>
        {label}<ChevronDown className="size-3.5" aria-hidden="true" />
      </summary>
      <div
        role="group"
        aria-label={label}
        className="absolute right-0 top-full z-40 mt-2 flex min-w-48 flex-col gap-1 rounded-lg border border-border bg-popover p-1.5 text-popover-foreground shadow-md [&>button]:h-8 [&>button]:w-full [&>button]:justify-start [&>button]:border-transparent"
        onClick={(event) => {
          if ((event.target as HTMLElement).closest("button, a")) close();
        }}
      >
        {children}
      </div>
    </details>
  );
}
