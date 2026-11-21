import { useEffect, useState, type ReactNode } from "react";
import { AlertTriangle, Ban, BookOpen, Bookmark, ChevronRight, ChevronsUpDown, Container, GitBranch, Sparkles, Terminal, type LucideIcon } from "lucide-react";
import type { BlackboardMode } from "@/lib/api";
import { Card, Label, Select, type SelectProps } from "@/components/ui";
import { SectionLabel } from "@/components/shared";
import { cn } from "@/lib/utils";
import { displayReasoningEffort, REASONING_EFFORT_VALUES } from "@/pages/runtimeProfileForm";
import { canPreviewLaunchSkills } from "@/pages/taskLaunchSkills";
import { type RuntimeLaunchController, launchUnavailableReason } from "./useRuntimeLaunchControls";
import { PreflightCard } from "./LaunchPreflightCard";
import { LaunchSkillsPreviewCard } from "./LaunchSummaryRail";

export function RuntimeLaunchControls({
  controller,
  ownerLabel,
  initialInput,
  allowDisabledBlackboardMode = true,
}: {
  controller: RuntimeLaunchController;
  ownerLabel: "task" | "session";
  initialInput: string;
  allowDisabledBlackboardMode?: boolean;
}) {
  const {
    form,
    setForm,
    presetId,
    presetMode,
    presetOpen,
    setPresetOpen,
    hostActivated,
    setHostActivated,
    containerCLI,
    setContainerCLI,
    sandboxNetwork,
    setSandboxNetwork,
    sandboxVPNTun,
    setSandboxVPNTun,
    blackboardMode,
    setBlackboardMode,
    preflight,
    setPreflight,
    skillsProfileId,
    skillsPreview,
    skillsPreviewLoading,
    skillsPreviewError,
    enabledSkillsPreview,
    launchRuntimePlugins,
    launchProfiles,
    compatibleProviders,
    modelOptions,
    error,
    updateRuntime,
    updateModelProvider,
    updatePreset,
  } = controller;
  useEffect(() => {
    if (!allowDisabledBlackboardMode && blackboardMode === "disabled") {
      setBlackboardMode("working_graph");
      setPreflight(null);
    }
  }, [allowDisabledBlackboardMode, blackboardMode, setBlackboardMode, setPreflight]);
  const hostRunner = form.runner === "host";
  const hostBlocked = hostRunner && !hostActivated;
  const unavailableReason = launchUnavailableReason({
    input: initialInput,
    inputLabel: ownerLabel === "task" ? "Task goal" : "Session goal",
    form,
    presetMode,
    compatibleProviderCount: compatibleProviders.length,
    hostBlocked,
  });
  // UI value maps Docker/Podman (container engines) + Host; backend still uses runner=sandbox|host.
  const runnerSelectValue = form.runner === "host" ? "host" : containerCLI;
  const engineLabel = form.runner === "host" ? "host" : containerCLI;
  const runnerSummary = form.runner === "host"
    ? "Host"
    : `${engineLabel === "podman" ? "Podman" : "Docker"} · ${sandboxNetwork === "host_proxy_only" ? "Host proxy only" : "Default bridge"} · VPN TUN ${sandboxVPNTun ? "on" : "off"}`;
  const skillsSummary = skillsPreviewLoading
    ? "Loading…"
    : skillsPreviewError
      ? "Error"
      : `${enabledSkillsPreview.length} enabled`;
  const blackboardModeCards: { mode: BlackboardMode; icon: LucideIcon; title: string; description: string; disabled: boolean }[] = [
    { mode: "working_graph", icon: Sparkles, title: "FGS", description: "The Runtime publishes Goals, Steps, and Facts. The Harness records accepted updates and Receipts.", disabled: false },
    ...(allowDisabledBlackboardMode
      ? [{ mode: "disabled" as const, icon: Ban, title: "Disabled", description: ownerLabel === "task" ? "This Task does not write to the Blackboard." : "This Session does not write to the Blackboard.", disabled: false }]
      : []),
  ];

  return (
    <>
      <section className="rounded-lg border border-border bg-card shadow-sm">
        <div className="border-b border-border px-4 py-3">
          <span className="text-sm font-medium">Runtime and model</span>
        </div>
        <div className="grid grid-cols-2 gap-x-4 gap-y-4 p-4">
          <div>
            <Label htmlFor="launch-runtime">Runtime</Label>
            <ControlSelect leadingIcon={Terminal} id="launch-runtime" name="runtime" value={form.runtime} disabled={presetMode} onChange={(event) => updateRuntime(event.target.value)}>
              {launchRuntimePlugins.map((plugin) => <option key={plugin.id} value={plugin.id}>{plugin.name}</option>)}
            </ControlSelect>
          </div>
          <div>
            <Label htmlFor="launch-model-provider">Model provider</Label>
            <ControlSelect id="launch-model-provider" name="model_provider" value={form.modelProviderId} disabled={presetMode} onChange={(event) => updateModelProvider(event.target.value)}>
              {compatibleProviders.length === 0
                ? <option value="">No compatible providers</option>
                : compatibleProviders.map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
            </ControlSelect>
          </div>
          <div>
            <Label htmlFor="launch-model">Model</Label>
            <ControlSelect
              id="launch-model"
              name="model"
              value={form.modelOverride}
              onChange={(event) => { setForm((current) => ({ ...current, modelOverride: event.target.value })); setPreflight(null); }}
              disabled={!presetMode && modelOptions.length === 0}
            >
              {modelOptions.length === 0 ? (
                <option value="">{form.modelOverride || "Default model"}</option>
              ) : (
                <>
                  {form.modelOverride && !modelOptions.includes(form.modelOverride) && <option value={form.modelOverride}>{form.modelOverride}</option>}
                  {modelOptions.map((model) => <option key={model} value={model}>{model}</option>)}
                </>
              )}
            </ControlSelect>
          </div>
          <div>
            <Label id="launch-reasoning-effort-label">Reasoning effort</Label>
            <div role="group" aria-labelledby="launch-reasoning-effort-label" className="mt-1.5 flex rounded-lg border border-input p-0.5">
              {REASONING_EFFORT_VALUES.map((effort) => (
                <button
                  key={effort}
                  type="button"
                  aria-pressed={displayReasoningEffort(form.reasoningEffort) === effort}
                  onClick={() => { setForm((current) => ({ ...current, reasoningEffort: effort })); setPreflight(null); }}
                  className={cn(
                    "flex-1 rounded-md px-2 py-1 text-xs transition-colors",
                    displayReasoningEffort(form.reasoningEffort) === effort
                      ? "bg-primary font-medium text-primary-foreground"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {effort}
                </button>
              ))}
            </div>
          </div>
        </div>
      </section>

      <div>
        <div className="mb-4 rounded-lg border border-border bg-card p-4">
          <div className="flex items-center gap-2">
            <input
              type="checkbox"
              id="launch-host-browser"
              checked={controller.hostBrowserEnabled}
              aria-describedby="launch-host-browser-boundary"
              onChange={(event) => { controller.setHostBrowserEnabled(event.target.checked); setPreflight(null); }}
              className="h-4 w-4 accent-primary"
            />
            <label htmlFor="launch-host-browser" className="text-sm font-medium">Enable Host Browser</label>
          </div>
          <p id="launch-host-browser-boundary" className="mt-2 text-xs text-muted-foreground">
            Use the host browser&apos;s real session. Playwright MCP can execute Runtime code in the host service process.
            This does not select Host Runner. When off, use the Sandbox Browser with a temporary session.
          </p>
        </div>
        <SectionLabel className="mb-2 px-0.5">Advanced configuration</SectionLabel>
        <div className="space-y-2">
          <ConfigAccordion icon={Container} title="Runner and network" summary={runnerSummary}>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div>
                <Label htmlFor="launch-runner">Runner</Label>
                <ControlSelect
                  id="launch-runner"
                  name="runner"
                  value={runnerSelectValue}
                  onChange={(event) => {
                    const value = event.target.value;
                    if (value === "host") {
                      setForm((current) => ({ ...current, runner: "host" }));
                    } else {
                      setForm((current) => ({ ...current, runner: "sandbox" }));
                      setContainerCLI(value === "podman" ? "podman" : "docker");
                      setHostActivated(false);
                    }
                    setPreflight(null);
                  }}
                >
                  <option value="docker">Docker</option>
                  <option value="podman">Podman</option>
                  <option value="host">Host</option>
                </ControlSelect>
                {form.runner === "sandbox" && (
                  <p className="mt-1 text-xs text-muted-foreground">
                    Container engine <span className="font-mono">{engineLabel}</span> runs the isolated task workdir.
                  </p>
                )}
              </div>
              {form.runner === "sandbox" && (
                <div>
                  <Label htmlFor="launch-sandbox-network">{engineLabel === "podman" ? "Podman network" : "Docker network"}</Label>
                  <ControlSelect
                    id="launch-sandbox-network"
                    name="sandbox_network"
                    value={sandboxNetwork}
                    onChange={(event) => {
                      const next = event.target.value;
                      setSandboxNetwork(next);
                      // host_proxy_only drops NET_ADMIN after the firewall is installed,
                      // so it cannot host an in-container OpenVPN TUN client.
                      if (next === "host_proxy_only") setSandboxVPNTun(false);
                      setPreflight(null);
                    }}
                  >
                    <option value="">Default bridge</option>
                    <option value="host_proxy_only">Host proxy only</option>
                  </ControlSelect>
                </div>
              )}
            </div>
            {form.runner === "sandbox" && (
              <label className="mt-3 flex items-start gap-2 text-sm">
                <input
                  type="checkbox"
                  id="launch-sandbox-vpn-tun"
                  name="sandbox_vpn_tun"
                  checked={sandboxVPNTun}
                  disabled={sandboxNetwork === "host_proxy_only"}
                  onChange={(event) => {
                    setSandboxVPNTun(event.target.checked);
                    setPreflight(null);
                  }}
                  className="mt-0.5 h-4 w-4 accent-primary"
                />
                <span>
                  <span className="font-medium">VPN TUN</span>
                  <span className="mt-0.5 block text-xs text-muted-foreground">
                    Mount <span className="font-mono">/dev/net/tun</span> and grant{" "}
                    <span className="font-mono">NET_ADMIN</span> in the{" "}
                    <span className="font-mono">{engineLabel}</span> container so OpenVPN can create{" "}
                    <span className="font-mono">tun0</span>. Unavailable with host proxy only.
                  </span>
                </span>
              </label>
            )}
          </ConfigAccordion>

          <ConfigAccordion icon={GitBranch} title="Blackboard mode" summary={blackboardMode === "disabled" ? "Disabled" : "FGS"}>
            <div role="radiogroup" aria-label="Blackboard conclusions" className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              {blackboardModeCards.map((card) => {
                const CardIcon = card.icon;
                const selected = blackboardMode === card.mode;
                return (
                  <button
                    key={card.mode}
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    disabled={card.disabled}
                    onClick={() => { setBlackboardMode(card.mode); setPreflight(null); }}
                    className={cn(
                      "rounded-lg border-2 p-3 text-left transition-colors",
                      selected ? "border-primary bg-primary/[0.03]" : "border-border hover:border-ring",
                      card.disabled && "cursor-not-allowed opacity-50",
                    )}
                  >
                    <div className="flex items-center gap-2 text-sm font-medium"><CardIcon className="h-4 w-4" />{card.title}</div>
                    <p className="mt-1 text-xs text-muted-foreground">{card.description}</p>
                  </button>
                );
              })}
            </div>
            <p className="mt-2 text-xs text-muted-foreground">
              {blackboardMode === "disabled"
                ? "The Runtime does not receive Blackboard state or Blackboard access. All non-Blackboard launch context remains available."
                : "The Runtime reads accepted FGS state and publishes updates through the Outbox. Check Receipts for acceptance."}
            </p>
          </ConfigAccordion>

          {launchProfiles.length > 0 && (
            <ConfigAccordion
              icon={Bookmark}
              title="Use a saved Runtime Profile"
              summary={presetMode ? (controller.profiles.find((profile) => profile.id === presetId)?.name ?? presetId) : "Not selected"}
              open={presetOpen}
              onOpenChange={setPresetOpen}
            >
              <div className="space-y-2">
                <Label htmlFor="launch-preset">Runtime Profile</Label>
                <ControlSelect id="launch-preset" name="runtime_profile" value={presetId} onChange={(event) => updatePreset(event.target.value)}>
                  <option value="">Direct configuration</option>
                  {launchRuntimePlugins.map((plugin) => {
                    const group = launchProfiles.filter((profile) => profile.provider === plugin.id);
                    if (group.length === 0) return null;
                    return (
                      <optgroup key={plugin.id} label={plugin.name}>
                        {group.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
                      </optgroup>
                    );
                  })}
                </ControlSelect>
                <p className="text-xs text-muted-foreground">A Runtime Profile copies its full advanced configuration into this new {ownerLabel}, and the Runtime changes to the Runtime of the Profile. Later Profile edits do not change it.</p>
              </div>
            </ConfigAccordion>
          )}

          {canPreviewLaunchSkills(form, presetId) && (
            <ConfigAccordion icon={BookOpen} title="Skills" summary={skillsSummary}>
              <LaunchSkillsPreviewCard presetMode={presetMode} profileId={skillsProfileId} loading={skillsPreviewLoading} error={skillsPreviewError} skills={enabledSkillsPreview} ready={skillsPreview !== null} />
            </ConfigAccordion>
          )}
        </div>
      </div>
      {hostRunner && (
        <Card className="border-warning bg-warning/10 p-3 space-y-2">
          <div className="flex items-center gap-2 text-warning"><AlertTriangle className="h-4 w-4" /><span className="text-sm font-medium">HOST runner — runs on your machine</span></div>
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" name="host_runner_acknowledged" checked={hostActivated} onChange={(event) => { setHostActivated(event.target.checked); setPreflight(null); }} className="mt-0.5 h-4 w-4 accent-warning" />
            <span>I explicitly activate the host runner for this {ownerLabel}. Commands execute on this machine outside the sandbox.</span>
          </label>
        </Card>
      )}

      {preflight && <PreflightCard preflight={preflight} />}
      {error && <p className="text-sm text-destructive">{error}</p>}
      {unavailableReason && (
        <p className="flex items-start gap-2 rounded-md border border-warning/30 bg-warning/10 p-3 text-sm text-warning">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" /><span>{unavailableReason}</span>
        </p>
      )}
    </>
  );
}

function ControlSelect({ leadingIcon: LeadingIcon, className, children, ...props }: SelectProps & { leadingIcon?: LucideIcon }) {
  return (
    <div className="relative mt-1.5">
      {LeadingIcon && <LeadingIcon className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />}
      <Select
        {...props}
        className={cn("h-[34px] appearance-none rounded-lg bg-card px-2.5 pr-8 text-[13px] shadow-none", LeadingIcon && "pl-8", className)}
      >
        {children}
      </Select>
      <ChevronsUpDown className="pointer-events-none absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
    </div>
  );
}

function ConfigAccordion({
  icon: Icon,
  title,
  summary,
  children,
  defaultOpen,
  open,
  onOpenChange,
}: {
  icon: LucideIcon;
  title: string;
  summary: string;
  children: ReactNode;
  defaultOpen?: boolean;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}) {
  const [internalOpen, setInternalOpen] = useState(defaultOpen ?? false);
  const isOpen = open ?? internalOpen;
  return (
    <div className="rounded-lg border border-border">
      <button
        type="button"
        aria-expanded={isOpen}
        onClick={() => {
          const next = !isOpen;
          if (open === undefined) setInternalOpen(next);
          onOpenChange?.(next);
        }}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-left"
      >
        <ChevronRight className={cn("h-4 w-4 text-muted-foreground transition-transform", isOpen && "rotate-90")} />
        <Icon className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium">{title}</span>
        <span className="ml-auto text-xs text-muted-foreground">{summary}</span>
      </button>
      {isOpen && <div className="border-t border-border px-4 py-3">{children}</div>}
    </div>
  );
}
