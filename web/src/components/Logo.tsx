import { cn } from "@/lib/utils";

/*
 * CyberPenda brand mark — user-provided transparent PNG in public/. Keeping the
 * Logo component API (className/bordered/spin) lets the app shell and tests stay
 * stable while the mark itself uses the real CyberPenda panda asset.
 */
export function Logo({
  className,
  bordered = false,
  spin = false,
}: {
  className?: string;
  bordered?: boolean;
  spin?: boolean;
}) {
  const mark = (
    <img
      src="/cyberpenda-logo.png"
      alt="CyberPenda"
      width={96}
      height={96}
      fetchPriority="high"
      decoding="async"
      className={cn("object-contain", spin && "logo-entrance-spin", className)}
    />
  );

  if (!bordered) return mark;

  return (
    <span className="inline-flex items-center justify-center rounded-md border border-border p-1.5">
      {mark}
    </span>
  );
}
