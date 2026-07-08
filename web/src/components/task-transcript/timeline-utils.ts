import type { EventColor, TimelineItem } from "./types";

export function getEventColor(item: TimelineItem): EventColor {
  switch (item.type) {
    case "text":
      return "agent";
    case "thinking":
      return "thinking";
    case "tool_use":
      return "tool";
    case "tool_result":
      return "result";
    case "error":
      return "error";
    case "lifecycle":
    case "steering":
      return "result";
    default:
      return "result";
  }
}

export const colorClasses: Record<EventColor, { bg: string; bgActive: string; label: string }> = {
  agent: { bg: "bg-emerald-400/60", bgActive: "bg-emerald-500", label: "bg-emerald-500/20 text-emerald-700 dark:text-emerald-300" },
  thinking: { bg: "bg-violet-400/60", bgActive: "bg-violet-500", label: "bg-violet-500/20 text-violet-700 dark:text-violet-300" },
  tool: { bg: "bg-blue-400/60", bgActive: "bg-blue-500", label: "bg-blue-500/20 text-blue-700 dark:text-blue-300" },
  result: { bg: "bg-slate-300/60 dark:bg-slate-600/60", bgActive: "bg-slate-400 dark:bg-slate-500", label: "bg-muted text-muted-foreground" },
  error: { bg: "bg-red-400/60", bgActive: "bg-red-500", label: "bg-red-500/20 text-red-700 dark:text-red-300" },
};

export function getEventLabel(item: TimelineItem): string {
  switch (item.type) {
    case "text":
      return "Agent";
    case "thinking":
      return "Thinking";
    case "tool_use":
      return item.tool ?? "Tool";
    case "tool_result":
      return item.tool ? item.tool : "Result";
    case "error":
      return "Error";
    case "lifecycle":
      return "Lifecycle";
    case "steering":
      return "Steering";
    default:
      return "Event";
  }
}

export function getEventSummary(item: TimelineItem): string {
  switch (item.type) {
    case "text":
      return item.content?.split("\n").find((line) => line.trim().length > 0) ?? "";
    case "thinking":
      return item.content?.slice(0, 200) ?? "";
    case "tool_use": {
      if (!item.input) return "";
      const inp = item.input as Record<string, string>;
      if (inp.query) return inp.query;
      if (inp.file_path) return shortenPath(inp.file_path);
      if (inp.path) return shortenPath(inp.path);
      if (inp.pattern) return inp.pattern;
      if (inp.description) return String(inp.description);
      if (inp.command) {
        const cmd = String(inp.command);
        return cmd.length > 120 ? cmd.slice(0, 120) + "…" : cmd;
      }
      if (inp.prompt) {
        const prompt = String(inp.prompt);
        return prompt.length > 120 ? prompt.slice(0, 120) + "…" : prompt;
      }
      if (inp.skill) return String(inp.skill);
      for (const value of Object.values(inp)) {
        if (typeof value === "string" && value.length > 0 && value.length < 120) return value;
      }
      return "";
    }
    case "tool_result":
      return item.output?.slice(0, 200) ?? "";
    case "error":
      return item.content ?? "";
    case "lifecycle":
    case "steering":
      return item.content ?? "";
    default:
      return "";
  }
}

export function shortenPath(path: string): string {
  const parts = path.split("/");
  if (parts.length <= 3) return path;
  return "…/" + parts.slice(-2).join("/");
}

export function itemFilterKey(item: TimelineItem): string {
  return item.tool && (item.type === "tool_use" || item.type === "tool_result")
    ? `tool:${item.tool}`
    : item.type;
}

export function buildFilterOptions(items: TimelineItem[]): [string, string][] {
  const options = new Map<string, string>();
  for (const item of items) {
    if (item.tool && (item.type === "tool_use" || item.type === "tool_result")) {
      const key = `tool:${item.tool}`;
      if (!options.has(key)) options.set(key, item.tool);
    } else {
      const value = item.type;
      if (!options.has(value)) {
        options.set(value, getEventLabel(item));
      }
    }
  }
  return Array.from(options.entries()).sort((a, b) => a[1].localeCompare(b[1]));
}

export function formatDuration(start: string, end: string): string {
  const ms = new Date(end).getTime() - new Date(start).getTime();
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const secs = seconds % 60;
  return `${minutes}m ${secs}s`;
}

export function formatElapsedMs(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const secs = seconds % 60;
  return `${minutes}m ${secs}s`;
}
