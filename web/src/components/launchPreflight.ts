import type { PreflightResult } from "@/lib/api";

export const SANDBOX_PREFLIGHT_CHECKS = new Set([
  "container_engines",
  "container_engine",
  "sandbox_runtime_root",
  "sandbox_vpn_tun",
]);

export const PREFLIGHT_CHECK_LABELS: Record<string, string> = {
  runtime_configuration: "Runtime configuration",
  runtime_profile: "Runtime profile",
  custom_args: "Custom args",
  skills: "Skills",
  runtime_extensions: "Extension packages",
  runtime_extension_requirements: "Extension requirements",
  model_provider: "Model provider",
  runner: "Runner",
  host_activation: "Host activation",
  credentials: "Credentials",
  container_engines: "Docker / Podman availability",
  container_engine: "Selected engine",
  sandbox_runtime_root: "Runtime root",
  sandbox_vpn_tun: "VPN TUN",
};

export function preflightCheckLabel(name: string) {
  return PREFLIGHT_CHECK_LABELS[name] ?? name.replaceAll("_", " ");
}

export function formatAPIKeyStatus(modelProvider: NonNullable<PreflightResult["model_provider"]>): string {
  if (modelProvider.api_key_source && modelProvider.api_key_env) return `${modelProvider.api_key_source} via ${modelProvider.api_key_env}`;
  return modelProvider.api_key_source || modelProvider.api_key_env || "not configured";
}
