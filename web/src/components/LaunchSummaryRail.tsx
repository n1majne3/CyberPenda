import { useEffect, useState } from "react";
import { Rocket, ShieldCheck } from "lucide-react";
import { apiGet, type CredentialBinding, type Skill } from "@/lib/api";
import { Button } from "@/components/ui";
import { displayReasoningEffort } from "@/pages/runtimeProfileForm";
import { launchSkillsPreviewDetail } from "@/pages/taskLaunchSkills";
import type { RuntimeLaunchController } from "./useRuntimeLaunchControls";

// Shared sticky launch-summary rail so the Task and Session launch surfaces
// cannot drift: summary rows + primary launch button + preflight preview.
export function LaunchSummaryRail({
  controller,
  disabled,
  busy,
  label,
  busyLabel = "Launching…",
  onClick,
  submit = false,
}: {
  controller: RuntimeLaunchController;
  disabled: boolean;
  busy: boolean;
  label: string;
  busyLabel?: string;
  onClick?: () => void;
  submit?: boolean;
}) {
  const {
    form,
    presetId,
    presetMode,
    profiles,
    plugins,
    compatibleProviders,
    containerCLI,
    blackboardMode,
    enabledSkillsPreview,
  } = controller;
  // Best-effort credential readiness preview: on failure, show the env name only.
  const [bindings, setBindings] = useState<CredentialBinding[] | null>(null);
  useEffect(() => {
    let cancelled = false;
    apiGet<{ bindings: CredentialBinding[] }>("/api/credential-bindings")
      .then((data) => {
        if (!cancelled) setBindings(data.bindings ?? []);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  const runtimeDisplay = plugins.find((plugin) => plugin.id === form.runtime)?.name ?? (form.runtime || "—");
  const summaryProvider = compatibleProviders.find((provider) => provider.id === form.modelProviderId);
  const modelDisplay = [summaryProvider?.name, form.modelOverride || "Default model"].filter(Boolean).join(" · ") || "—";
  const runnerDisplay = form.runner === "host" ? "Host" : containerCLI === "podman" ? "Podman" : "Docker";
  const blackboardDisplay = blackboardMode === "disabled" ? "Disabled" : "FGS";
  const profileDisplay = presetMode ? `Runtime Profile: ${profiles.find((profile) => profile.id === presetId)?.name ?? presetId}` : "Direct configuration";
  const apiKeyEnv = summaryProvider?.api_key_env ?? "";
  const credentialConfigured = bindings !== null && bindings.some((binding) => binding.credential_ref === apiKeyEnv && !binding.disabled);

  return (
    <aside className="h-fit lg:sticky lg:top-6">
      <div className="rounded-lg border border-border bg-card p-[19px]">
        <div className="mb-4"><span className="text-sm font-medium">Launch summary</span></div>
        <dl className="space-y-3 text-[11px]">
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Runtime</dt><dd className="min-w-0 truncate font-medium">{runtimeDisplay}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Model</dt><dd className="min-w-0 truncate font-mono">{modelDisplay}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Effort</dt><dd className="font-medium">{displayReasoningEffort(form.reasoningEffort)}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Runner</dt><dd className="font-medium">{runnerDisplay}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Blackboard</dt><dd className="font-medium">{blackboardDisplay}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Skills</dt><dd className="font-medium">{enabledSkillsPreview.length} enabled</dd></div>
        </dl>
        <div className="mt-5 border-t border-border pt-4">
          <Button type={submit ? "submit" : "button"} onClick={onClick} disabled={disabled} className="w-full">
            <Rocket className="h-4 w-4" /> {busy ? busyLabel : label}
          </Button>
          <p className="mt-2 text-center text-xs text-muted-foreground">Preflight checks run automatically before launch.</p>
        </div>
      </div>
      <div className="mt-5 text-[11px] leading-5 text-muted-foreground">
        <div className="flex items-center gap-2 font-medium text-foreground"><ShieldCheck className="h-4 w-4 text-success" />Preflight preview</div>
        <p className="mt-1">
          {profileDisplay}
          {apiKeyEnv && (
            <>
              {" · "}
              <span className="font-mono">{apiKeyEnv}</span>
              {bindings !== null && (credentialConfigured
                ? <span className="text-success"> configured</span>
                : <span className="text-warning"> not configured</span>)}
            </>
          )}
        </p>
      </div>
    </aside>
  );
}

export function LaunchSkillsPreviewCard({ presetMode, profileId, loading, error, skills, ready }: { presetMode: boolean; profileId: string; loading: boolean; error: string | null; skills: Skill[]; ready: boolean }) {
  return (
    <div>
      <p className="mb-3 text-xs text-muted-foreground">{launchSkillsPreviewDetail(presetMode)}</p>
      {profileId && <p className="mb-3 font-mono text-[11px] text-muted-foreground truncate">Profile: {profileId}</p>}
      {loading && <p className="text-sm text-muted-foreground">Loading enabled skills…</p>}
      {error && <p className="text-sm text-destructive">{error}</p>}
      {!loading && !error && ready && skills.length === 0 && <p className="text-sm text-muted-foreground">{profileId ? "No skills enabled for this profile." : "No matching skills profile yet."}</p>}
      {!loading && !error && skills.length > 0 && <div className="max-h-60 space-y-2 overflow-y-auto overscroll-y-contain pr-1" aria-label={`${skills.length} enabled skills`}>{skills.map((skill) => <div key={skill.id} className="rounded-lg border border-border/60 bg-background/50 p-2 text-sm"><div className="font-medium">{skill.name || skill.id}</div><div className="font-mono text-xs text-muted-foreground">{skill.id}</div></div>)}</div>}
    </div>
  );
}
