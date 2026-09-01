import { useRef, useState } from "react";
import { Paperclip, UploadCloud, X } from "lucide-react";
import { Label } from "@/components/ui";
import { cn } from "@/lib/utils";

const MAX_ATTACHMENT_COUNT = 25;
const MAX_ATTACHMENT_MB = 100;
const MAX_ATTACHMENT_BYTES = MAX_ATTACHMENT_MB * 1024 * 1024;

function formatAttachmentBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function AttachmentFileRow({
  name,
  size,
  prefix,
  onRemove,
}: {
  name: string;
  size?: number;
  prefix?: string;
  onRemove?: () => void;
}) {
  return (
    <div className="flex items-center justify-between gap-2 rounded-md border border-border/60 bg-background/50 px-2 py-1 text-sm">
      <span className="flex min-w-0 items-center gap-2">
        <Paperclip className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="truncate">{prefix ? `${prefix} ${name}` : name}</span>
        {typeof size === "number" && (
          <span className="shrink-0 text-xs text-muted-foreground">{formatAttachmentBytes(size)}</span>
        )}
      </span>
      {onRemove && (
        <button
          type="button"
          aria-label={`Remove ${name}`}
          onClick={onRemove}
          className="shrink-0 text-muted-foreground hover:text-destructive"
        >
          <X className="h-4 w-4" aria-hidden="true" />
        </button>
      )}
    </div>
  );
}

export function AttachmentPicker({
  id,
  files,
  onFilesChange,
  onError,
  ownerLabel,
  variant = "default",
}: {
  id: string;
  files: File[];
  onFilesChange: (files: File[]) => void;
  onError?: (message: string | null) => void;
  ownerLabel: string;
  variant?: "default" | "compact";
}) {
  const [dragOver, setDragOver] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  function addFiles(incoming: FileList | File[]) {
    onError?.(null);
    const next = [...files];
    for (const file of Array.from(incoming)) {
      if (file.size > MAX_ATTACHMENT_BYTES) {
        onError?.(`${file.name} exceeds the ${MAX_ATTACHMENT_MB} MB limit`);
        continue;
      }
      if (next.length >= MAX_ATTACHMENT_COUNT) {
        onError?.(`At most ${MAX_ATTACHMENT_COUNT} attachments per ${ownerLabel}`);
        break;
      }
      if (next.some((staged) => staged.name === file.name && staged.size === file.size)) continue;
      next.push(file);
    }
    onFilesChange(next);
  }

  const fileInput = (
    <input
      ref={fileInputRef}
      id={id}
      type="file"
      name="attachments"
      multiple
      className="hidden"
      onChange={(event) => {
        if (event.target.files && event.target.files.length > 0) addFiles(event.target.files);
        event.target.value = "";
      }}
    />
  );
  const dropHandlers = {
    onClick: () => fileInputRef.current?.click(),
    onKeyDown: (event: React.KeyboardEvent) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        fileInputRef.current?.click();
      }
    },
    onDragOver: (event: React.DragEvent) => {
      event.preventDefault();
      setDragOver(true);
    },
    onDragLeave: () => setDragOver(false),
    onDrop: (event: React.DragEvent) => {
      event.preventDefault();
      setDragOver(false);
      if (event.dataTransfer.files.length > 0) addFiles(event.dataTransfer.files);
    },
  };
  const fileList = files.length > 0 && (
    <ul className="mt-2 space-y-1">
      {files.map((file, index) => (
        <li key={`${file.name}-${file.size}`}>
          <AttachmentFileRow
            name={file.name}
            size={file.size}
            onRemove={() => onFilesChange(files.filter((_, position) => position !== index))}
          />
        </li>
      ))}
    </ul>
  );

  if (variant === "compact") {
    return (
      <div>
        <Label htmlFor={id} className="sr-only">Attachments</Label>
        <div
          data-testid="attachment-dropzone"
          role="button"
          tabIndex={0}
          {...dropHandlers}
          className={cn(
            "flex cursor-pointer items-center justify-between gap-3 rounded-lg border border-dashed px-3.5 py-2.5 transition-colors",
            dragOver ? "border-primary bg-primary/10" : "border-input bg-muted/30",
          )}
        >
          <div className="flex min-w-0 items-center gap-2.5 text-xs text-muted-foreground">
            <Paperclip className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span>
              Drag files here, or <span className="font-medium text-foreground underline underline-offset-2">browse</span> · up to {MAX_ATTACHMENT_COUNT} files, {MAX_ATTACHMENT_MB} MB each
            </span>
          </div>
          <span className="shrink-0 text-[11px] text-muted-foreground">Projected into the {ownerLabel} workdir</span>
          {fileInput}
        </div>
        {fileList}
      </div>
    );
  }

  return (
    <div>
      <Label htmlFor={id}>Attachments</Label>
      <div
        data-testid="attachment-dropzone"
        role="button"
        tabIndex={0}
        {...dropHandlers}
        className={`mt-1 flex cursor-pointer flex-col items-center justify-center gap-1 rounded-lg border border-dashed p-4 text-sm transition-colors ${
          dragOver ? "border-primary bg-primary/10" : "border-border bg-background/50"
        }`}
      >
        <UploadCloud className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
        <span className="text-muted-foreground">Drag &amp; drop files here, or click to browse</span>
        <span className="text-xs text-muted-foreground">
          Up to {MAX_ATTACHMENT_COUNT} files, {MAX_ATTACHMENT_MB} MB each. Projected into the {ownerLabel} workdir.
        </span>
        {fileInput}
      </div>
      {fileList}
    </div>
  );
}
