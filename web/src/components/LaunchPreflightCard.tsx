import type { PreflightResult } from "@/lib/api";
import { Card } from "@/components/ui";
import { CheckCircle2, XCircle } from "lucide-react";
import { SANDBOX_PREFLIGHT_CHECKS, preflightCheckLabel, formatAPIKeyStatus } from "./launchPreflight";

export function PreflightCheckRow({ check }: { check: PreflightResult["checks"][number] }) {
  const label = preflightCheckLabel(check.name);
  return (
    <div className="flex items-start gap-2 text-sm" data-preflight-check={check.name}>
      {check.status === "pass" ? (
        <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-emerald-400" aria-hidden="true" />
      ) : (
        <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" aria-hidden="true" />
      )}
      <div className="min-w-0">
        <span className="font-medium">{label}</span>
        {check.detail && (
          <span className="text-muted-foreground">
            : <span className={SANDBOX_PREFLIGHT_CHECKS.has(check.name) ? "break-all font-mono text-xs" : undefined}>{check.detail}</span>
          </span>
        )}
      </div>
    </div>
  );
}

export function PreflightCard({ preflight }: { preflight: PreflightResult }) {
  const sandboxChecks = preflight.checks.filter((check) => SANDBOX_PREFLIGHT_CHECKS.has(check.name));
  const otherChecks = preflight.checks.filter((check) => !SANDBOX_PREFLIGHT_CHECKS.has(check.name));

  return (
    <Card
      className={preflight.pass ? "border-emerald-500/40 bg-emerald-500/5 p-3" : "border-destructive/40 bg-destructive/5 p-3"}
      aria-label={preflight.pass ? "Preflight passed" : "Preflight failed"}
    >
      <div className="space-y-2">
        {otherChecks.map((check) => (
          <PreflightCheckRow key={check.name} check={check} />
        ))}
      </div>
      {sandboxChecks.length > 0 && (
        <div className="mt-3 border-t border-border/60 pt-3" data-preflight-section="sandbox-environment">
          <p className="mb-2 text-sm font-medium">Container environment</p>
          <div className="space-y-2 rounded-lg border border-border/60 bg-background/50 p-2">
            {sandboxChecks.map((check) => (
              <PreflightCheckRow key={check.name} check={check} />
            ))}
          </div>
        </div>
      )}
      {preflight.model_provider && (
        <div className="mt-3 border-t border-border/60 pt-3">
          <p className="mb-2 text-sm font-medium">Model provider</p>
          <div className="rounded-lg border border-border/60 bg-background/50 p-2 text-sm space-y-1">
            <div className="font-medium">{preflight.model_provider.model_provider_name || preflight.model_provider.model_provider_id}</div>
            <div className="font-mono text-xs text-muted-foreground">{preflight.model_provider.model} via {preflight.model_provider.protocol}</div>
            <div className="text-xs text-muted-foreground">{preflight.model_provider.endpoint_base_url ?? preflight.model_provider.base_url}</div>
            <div className="font-mono text-xs text-muted-foreground">API key: {formatAPIKeyStatus(preflight.model_provider)}</div>
          </div>
        </div>
      )}
      {preflight.codex_multi_agent && (
        <div className="mt-3 border-t border-border/60 pt-3" data-preflight-section="codex-multi-agent">
          <p className="mb-2 text-sm font-medium">Codex multi-agent tools</p>
          <div className="rounded-lg border border-border/60 bg-background/50 p-2 text-xs space-y-1">
            <div className="text-sm">
              {preflight.codex_multi_agent.state === "on" && "Spawn tools on for this launch"}
              {preflight.codex_multi_agent.state === "off" && "Spawn tools off for this launch"}
              {preflight.codex_multi_agent.state === "inherit" && "Codex decides — no multi-agent keys projected"}
            </div>
            {preflight.codex_multi_agent.state === "on" && (
              <div className="font-mono text-muted-foreground">
                {[
                  preflight.codex_multi_agent.max_concurrent_threads_per_session
                    ? `max threads ${preflight.codex_multi_agent.max_concurrent_threads_per_session}`
                    : null,
                  preflight.codex_multi_agent.max_depth ? `max depth ${preflight.codex_multi_agent.max_depth}` : null,
                ]
                  .filter(Boolean)
                  .join(" · ") || "Codex default caps"}
              </div>
            )}
            <p className="text-muted-foreground">Spawn stays a model tool inside the runtime turn; CyberPenda schedules no subagents.</p>
          </div>
        </div>
      )}
      {preflight.runtime_extensions && preflight.runtime_extensions.length > 0 && (
        <div className="mt-3 border-t border-border/60 pt-3">
          <p className="mb-2 text-sm font-medium">Runtime extensions</p>
          <div className="space-y-2">{preflight.runtime_extensions.map((extension) => <div key={extension.id} className="rounded-lg border border-border/60 bg-background/50 p-2 text-sm"><div className="font-medium">{extension.name || extension.id}</div><div className="font-mono text-xs text-muted-foreground">{extension.id}</div>{extension.source === "catalog" && extension.install_ref && <div className="text-xs text-muted-foreground">Install: {extension.install_ref}</div>}</div>)}</div>
        </div>
      )}
      {preflight.skills && preflight.skills.length > 0 && (
        <div className="mt-3 border-t border-border/60 pt-3">
          <p className="mb-2 text-sm font-medium">Enabled Skills</p>
          <div className="max-h-60 space-y-2 overflow-y-auto overscroll-y-contain pr-1">{preflight.skills.map((skill) => <div key={skill.id} className="rounded-lg border border-border/60 bg-background/50 p-2 text-sm"><div className="font-medium">{skill.name || skill.id}</div><div className="font-mono text-xs text-muted-foreground">{skill.id}</div></div>)}</div>
        </div>
      )}
    </Card>
  );
}
