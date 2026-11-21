export const SOURCE_KIND_LABELS: Record<string, string> = {
  env: "Environment variable",
  literal: "Literal secret",
};

export function formatSourceDisplay(kind: string, value?: string): string {
  if (!value) return "";
  if (kind === "literal") return "••••••••";
  return value;
}
