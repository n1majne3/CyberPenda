import type { TaskEvent, TaskTranscriptEntry } from "@/lib/api";

// Timeline is a diagnostic trace: lifecycle, steering, and high-level messages.
// Tool calls and results live in Conversation only.
export function shouldShowInTimeline(event: TaskEvent): boolean {
  if (event.kind !== "runtime_output") {
    return true;
  }
  const text = stringValue(event.payload?.text);
  if (!text.trim()) {
    return false;
  }
  return !isToolOnlyRuntimeOutput(text);
}

export function summarizeTaskEvent(event: TaskEvent): string {
  const payload = event.payload ?? {};

  if (event.kind === "lifecycle") {
    const phase = stringValue(payload.phase);
    const adapter = stringValue(payload.adapter);
    switch (phase) {
      case "started":
        return adapter ? `Started · ${adapter}` : "Started";
      case "completed":
        return adapter ? `Completed · ${adapter}` : "Completed";
      case "failed":
        return adapter ? `Failed · ${adapter}` : "Failed";
      case "stopped":
        return adapter ? `Stopped · ${adapter}` : "Stopped";
      case "process_started":
        return adapter ? `Process started · ${adapter}` : "Process started";
      default:
        return phase ? `Lifecycle · ${phase}` : "Lifecycle event";
    }
  }

  if (event.kind === "steering") {
    const directive = stringValue(payload.directive);
    return directive ? `Steering · ${firstLine(directive)}` : "Steering";
  }

  if (event.kind === "conversation") {
    return firstLine(stringValue(payload.text) || stringValue(payload.content) || stringValue(payload.message));
  }

  if (event.kind === "runtime_output") {
    const stream = stringValue(payload.stream) || "runtime";
    const text = stringValue(payload.text);
    if (!text) return `${stream} · (empty)`;
    return `${stream} · ${summarizeRuntimeText(text, { omitTools: true })}`;
  }

  return firstLine(JSON.stringify(payload));
}

export function collapsedTranscriptTitle(entry: TaskTranscriptEntry): string {
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
  for (const key of ["command", "file_path", "path", "query", "url", "pattern"]) {
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

type SummarizeRuntimeOptions = {
  omitTools?: boolean;
};

function summarizeRuntimeText(text: string, options: SummarizeRuntimeOptions = {}): string {
  const trimmed = text.trim();
  if (!trimmed.startsWith("{")) {
    return firstLine(trimmed);
  }

  let record: Record<string, unknown>;
  try {
    record = JSON.parse(trimmed) as Record<string, unknown>;
  } catch {
    return firstLine(trimmed);
  }

  const type = stringValue(record.type);
  if (type === "system") {
    const subtype = stringValue(record.subtype);
    return subtype ? `system ${subtype}` : "system event";
  }
  if (type === "result") {
    const subtype = stringValue(record.subtype) || (record.is_error ? "error" : "ok");
    return `run finished (${subtype})`;
  }
  if (type === "assistant") {
    const message = asRecord(record.message);
    const content = asArray(message?.content);
    const parts: string[] = [];
    for (const block of content) {
      const item = asRecord(block);
      if (!item) continue;
      const blockType = stringValue(item.type).toLowerCase();
      if (blockType === "text") {
        const line = firstLine(stringValue(item.text) || stringValue(item.content));
        if (line) parts.push(`assistant: ${line}`);
      } else if (!options.omitTools && (blockType === "tool_use" || blockType === "tool_call")) {
        parts.push(`tool_use ${stringValue(item.name) || "tool"}`);
      } else if (!options.omitTools && blockType === "thinking") {
        parts.push("thinking");
      }
    }
    if (parts.length > 0) return parts.join(", ");
    return "assistant event";
  }
  if (type === "user") {
    const message = asRecord(record.message);
    const content = asArray(message?.content);
    for (const block of content) {
      const item = asRecord(block);
      if (!item) continue;
      const blockType = stringValue(item.type).toLowerCase();
      if (blockType === "tool_result") {
        if (options.omitTools) {
          continue;
        }
        const preview = firstLine(stringValue(item.content) || stringValue(item.output));
        const err = item.is_error ? " (error)" : "";
        return preview ? `tool_result${err}: ${preview}` : `tool_result${err}`;
      }
    }
    return "user event";
  }
  if (type === "tool_call" || type === "function_call") {
    return options.omitTools ? "tool event" : `tool_call ${stringValue(record.name) || "tool"}`;
  }
  if (type === "tool_result" || type === "function_call_output") {
    if (options.omitTools) {
      return "tool event";
    }
    const preview = firstLine(stringValue(record.output) || stringValue(record.content));
    return preview ? `tool_result: ${preview}` : "tool_result";
  }

  return type ? `${type} event` : firstLine(trimmed);
}

function isToolOnlyRuntimeOutput(text: string): boolean {
  const trimmed = text.trim();
  if (!trimmed.startsWith("{")) {
    return false;
  }

  let record: Record<string, unknown>;
  try {
    record = JSON.parse(trimmed) as Record<string, unknown>;
  } catch {
    return false;
  }

  const type = stringValue(record.type).toLowerCase();
  if (type === "system") {
    return isIgnorableSystemSubtype(stringValue(record.subtype));
  }
  if (type === "tool_call" || type === "function_call" || type === "tool_use") {
    return true;
  }
  if (type === "tool_result" || type === "function_call_output") {
    return true;
  }
  if (type === "assistant") {
    return isToolOrThinkingOnlyContent(asArray(asRecord(record.message)?.content));
  }
  if (type === "user") {
    const content = asArray(asRecord(record.message)?.content);
    if (content.length === 0) {
      return false;
    }
    return content.every((block) => {
      const item = asRecord(block);
      if (!item) return false;
      const blockType = stringValue(item.type).toLowerCase();
      return blockType === "tool_result" || blockType === "tool_use";
    });
  }
  return false;
}

function isIgnorableSystemSubtype(subtype: string): boolean {
  const normalized = subtype.toLowerCase();
  if (normalized === "thinking_tokens" || normalized === "init") {
    return true;
  }
  return normalized.startsWith("task_");
}

function isToolOrThinkingOnlyContent(content: unknown[]): boolean {
  if (content.length === 0) {
    return false;
  }
  let sawMeaningful = false;
  for (const block of content) {
    const item = asRecord(block);
    if (!item) continue;
    const blockType = stringValue(item.type).toLowerCase();
    if (blockType === "text") {
      if (stringValue(item.text) || stringValue(item.content)) {
        sawMeaningful = true;
      }
      continue;
    }
    if (blockType === "tool_use" || blockType === "tool_call" || blockType === "thinking") {
      continue;
    }
    sawMeaningful = true;
  }
  return !sawMeaningful;
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function firstLine(value: string): string {
  return value.split(/\r?\n/, 1)[0] ?? "";
}
