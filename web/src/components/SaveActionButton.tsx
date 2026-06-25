import { Check, Loader2 } from "lucide-react";
import { Button, type ButtonProps } from "@/components/ui";
import { cn } from "@/lib/utils";

export type SaveActionButtonProps = {
  label?: string;
  pending?: boolean;
  saved?: boolean;
  disabled?: boolean;
  onClick?: () => void;
  size?: ButtonProps["size"];
  className?: string;
};

export function SaveActionButton({
  label = "Save",
  pending = false,
  saved = false,
  disabled = false,
  onClick,
  size = "sm",
  className,
}: SaveActionButtonProps) {
  const showSaved = saved && !pending;

  return (
    <div className={cn("inline-flex items-center gap-2", className)}>
      <span
        aria-live="polite"
        className={cn(
          "pointer-events-none text-xs font-medium text-success transition-all duration-200 ease-out",
          showSaved ? "translate-x-0 opacity-100" : "-translate-x-1 opacity-0 w-0 overflow-hidden",
        )}
      >
        Saved
      </span>
      <Button
        size={size}
        onClick={onClick}
        disabled={disabled || pending}
        className={cn(
          "min-w-[5.5rem] transition-[transform,background-color,box-shadow,opacity] duration-200 ease-out",
          showSaved && "bg-success/15 text-success hover:bg-success/20",
        )}
      >
        <span className="relative inline-flex items-center justify-center gap-1.5">
          {pending && (
            <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" />
          )}
          {showSaved && (
            <Check aria-hidden className="h-3.5 w-3.5 save-check-pop" />
          )}
          <span key={pending ? "pending" : showSaved ? "saved" : "idle"} className="save-label-in">
            {pending ? "Saving…" : showSaved ? "Saved" : label}
          </span>
        </span>
      </Button>
    </div>
  );
}