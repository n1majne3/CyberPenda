import type { TaskTranscriptEntry } from "@/lib/api";

// Transcript rendering helpers. Timeline classification and provider-record
// summarization live in the daemon (internal/timeline, internal/runtimeoutput);
// the client only renders what the server already parsed.

export function collapsedTranscriptTitle(entry: TaskTranscriptEntry): string {
  if (entry.kind === "reasoning") return "Reasoning";
  if (entry.kind === "tool_call") {
    const toolName = entry.tool_name || "Tool";
    const preview = toolInputPreview(entry);
    return preview ? `${toolName} · ${preview}` : toolName;
  }
  if (entry.kind === "tool_result") {
    const preview = entry.text ? ` · ${firstLine(entry.text)}` : "";
    return `Result${preview}`;
  }
  const prefix = entry.stream ? `Runtime output (${entry.stream})` : "Runtime output";
  return entry.text ? `${prefix}: ${firstLine(entry.text)}` : prefix;
}

function toolInputPreview(entry: TaskTranscriptEntry): string {
  const input = asRecord(asRecord(entry.details)?.input);
  if (!input) return "";
  for (const key of ["command", "file_path", "path", "query", "url", "pattern", "name"]) {
    const value = stringValue(input[key]);
    if (value) return firstLine(value);
  }
  return "";
}

export interface ToolCallField {
  label: string;
  value: string;
  // block fields render as multi-line code surfaces; the rest render inline.
  block: boolean;
}

// Argument keys that are almost always code/commands and read best in a
// monospaced block regardless of length.
const CODE_INPUT_KEYS = new Set([
  "command",
  "cmd",
  "code",
  "script",
  "content",
  "body",
  "payload",
  "stdin",
  "sql",
  "diff",
  "patch",
]);

// toolCallFields turns a tool call's raw JSON input into a flat, labeled list so
// the UI can render friendly key/value rows instead of a raw JSON envelope.
export function toolCallFields(entry: TaskTranscriptEntry): ToolCallField[] {
  const input = asRecord(asRecord(entry.details)?.input);
  if (!input) return [];
  const fields: ToolCallField[] = [];
  for (const [key, raw] of Object.entries(input)) {
    const value = formatToolValue(raw);
    if (value === "") continue;
    const block = CODE_INPUT_KEYS.has(key.toLowerCase()) || value.includes("\n") || value.length > 80;
    fields.push({ label: humanizeKey(key), value, block });
  }
  return fields;
}

function formatToolValue(value: unknown): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return "";
  }
}

function humanizeKey(key: string): string {
  const spaced = key
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/[_-]+/g, " ")
    .trim();
  return spaced
    .split(/\s+/)
    .map((word) => (word ? word[0]!.toUpperCase() + word.slice(1) : word))
    .join(" ");
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function firstLine(value: string): string {
  return value.split(/\r?\n/, 1)[0] ?? "";
}
