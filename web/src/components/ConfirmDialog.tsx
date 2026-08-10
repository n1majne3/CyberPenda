import { useEffect, useId, useRef } from "react";
import { Button, Card, CardDescription, CardTitle, Input } from "@/components/ui";

/**
 * Application-styled confirm dialog. Replaces native window.confirm so
 * destructive actions stay inside the design system (theming, focus
 * management, keyboard support) instead of falling back to a stock dialog.
 *
 * Rendered as a fixed overlay with role="alertdialog"; Escape and the Cancel
 * button dismiss without confirming. Focus moves to the dialog on open and
 * returns to the previously focused element on close.
 */
export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel = "Confirm",
  cancelLabel = "Cancel",
  destructive = false,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  title: string;
  description?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  destructive?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const titleId = useId();
  const descriptionId = useId();
  const cancelRef = useRef<HTMLButtonElement>(null);
  const restoreFocusRef = useRef<Element | null>(null);

  useEffect(() => {
    if (!open) return;
    restoreFocusRef.current = document.activeElement;
    // Land on Cancel so an accidental Enter/Space never confirms a destructive
    // action; focus returns to the trigger when the dialog closes.
    cancelRef.current?.focus();
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onCancel();
      }
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      window.removeEventListener("keydown", closeOnEscape);
      (restoreFocusRef.current as HTMLElement | null)?.focus?.();
    };
  }, [open, onCancel]);

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center overflow-y-auto bg-black/50 p-4"
      onClick={(event) => {
        if (event.target === event.currentTarget) onCancel();
      }}
    >
      <Card
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
        size="default"
        // Cap height to the viewport and keep actions pinned so a long Task
        // Goal cannot push Confirm/Cancel off-screen.
        className="my-auto flex max-h-[min(32rem,calc(100dvh-2rem))] w-full max-w-sm flex-col overflow-hidden outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <div className="min-h-0 flex-1 space-y-2 overflow-y-auto">
          <CardTitle id={titleId} className="break-words [overflow-wrap:anywhere]">
            {title}
          </CardTitle>
          {description && (
            <CardDescription id={descriptionId} className="break-words [overflow-wrap:anywhere]">
              {description}
            </CardDescription>
          )}
        </div>
        <div className="mt-3 flex shrink-0 justify-end gap-2">
          <Button ref={cancelRef} variant="ghost" onClick={onCancel}>
            {cancelLabel}
          </Button>
          <Button
            variant={destructive ? "destructive" : "default"}
            onClick={onConfirm}
          >
            {confirmLabel}
          </Button>
        </div>
      </Card>
    </div>
  );
}

/**
 * Application-styled prompt dialog for single-field input (e.g. rename).
 * Replaces native window.prompt: themed, focus-managed, Enter confirms,
 * Escape cancels, and the input is pre-selected for immediate retyping.
 */
export function PromptDialog({
  open,
  title,
  label,
  initialValue = "",
  confirmLabel = "Save",
  cancelLabel = "Cancel",
  onConfirm,
  onCancel,
}: {
  open: boolean;
  title: string;
  label: string;
  initialValue?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  onConfirm: (value: string) => void;
  onCancel: () => void;
}) {
  const titleId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const restoreFocusRef = useRef<Element | null>(null);

  useEffect(() => {
    if (!open) return;
    restoreFocusRef.current = document.activeElement;
    inputRef.current?.focus();
    inputRef.current?.select();
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onCancel();
      }
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      window.removeEventListener("keydown", closeOnEscape);
      (restoreFocusRef.current as HTMLElement | null)?.focus?.();
    };
  }, [open, onCancel]);

  if (!open) return null;

  const submit = () => {
    const value = inputRef.current?.value ?? "";
    if (value.trim()) onConfirm(value);
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center overflow-y-auto bg-black/50 p-4"
      onClick={(event) => {
        if (event.target === event.currentTarget) onCancel();
      }}
    >
      <Card
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="my-auto flex max-h-[min(32rem,calc(100dvh-2rem))] w-full max-w-sm flex-col overflow-hidden"
      >
        <div className="min-h-0 flex-1 space-y-2 overflow-y-auto">
          <CardTitle id={titleId} className="break-words [overflow-wrap:anywhere]">
            {title}
          </CardTitle>
          <div>
            <label className="sr-only" htmlFor="prompt-dialog-input">
              {label}
            </label>
            <Input
              ref={inputRef}
              id="prompt-dialog-input"
              aria-label={label}
              defaultValue={initialValue}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  submit();
                }
              }}
            />
          </div>
        </div>
        <div className="mt-3 flex shrink-0 justify-end gap-2">
          <Button variant="ghost" onClick={onCancel}>
            {cancelLabel}
          </Button>
          <Button onClick={submit}>{confirmLabel}</Button>
        </div>
      </Card>
    </div>
  );
}
