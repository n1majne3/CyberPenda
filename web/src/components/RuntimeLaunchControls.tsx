import { useEffect, useMemo, useState, type ReactNode } from "react";
import { AlertTriangle, Ban, BookOpen, Bookmark, CheckCircle2, ChevronRight, ChevronsUpDown, Container, GitBranch, Rocket, ShieldCheck, Sparkles, Terminal, UserCheck, XCircle, type LucideIcon } from "lucide-react";
import {
  apiGet,
  apiPost,
  type BlackboardMode,
  type CredentialBinding,
  type Health,
  type ModelProvider,
  type PreflightResult,
  type Project,
  type RuntimePlugin,
  type RuntimeProfile,
  type Skill,
} from "@/lib/api";
import { Button, Card, Label, Select, type SelectProps } from "@/components/ui";
import { SectionLabel } from "@/components/shared";
import { cn } from "@/lib/utils";
import { displayReasoningEffort, REASONING_EFFORT_VALUES, selectableModelProviders } from "@/pages/runtimeProfileForm";
import {
  canLaunch,
  formFromPreset,
  initialLaunchState,
  launchSelectionPayload,
  launchRuntimes,
  modelsForProvider,
  presetMatchesRuntime,
  presetsForRuntime,
  simpleLaunchFormForRuntime,
  type LaunchForm,
} from "@/pages/taskLaunchForm";
import {
  canPreviewLaunchSkills,
  enabledLaunchSkills,
  launchProfileIdForSkillsPreview,
  launchSkillsPreviewDetail,
} from "@/pages/taskLaunchSkills";

type RuntimeLaunchControlsOptions = {
  projectId?: string;
  defaultBlackboardMode?: BlackboardMode;
};

// The hook and its render surface intentionally live together so Task and
// Session launch behavior cannot drift behind a component-only abstraction.
// eslint-disable-next-line react-refresh/only-export-components
export function useRuntimeLaunchControls({ projectId, defaultBlackboardMode = "disabled" }: RuntimeLaunchControlsOptions = {}) {
  const [plugins, setPlugins] = useState<RuntimePlugin[]>([]);
  const [modelProviders, setModelProviders] = useState<ModelProvider[]>([]);
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [project, setProject] = useState<Project | null>(null);
  const [defaultRunner, setDefaultRunner] = useState("sandbox");
  const [form, setForm] = useState<LaunchForm>({
    runtime: "",
    modelProviderId: "",
    modelOverride: "",
    reasoningEffort: "high",
    runner: "sandbox",
  });
  const [presetId, setPresetId] = useState("");
  const [presetOpen, setPresetOpen] = useState(false);
  const [hostActivated, setHostActivated] = useState(false);
  // Container engine for sandbox launches: docker | podman (shown instead of bare "sandbox").
  const [containerCLI, setContainerCLI] = useState<"docker" | "podman">("docker");
  const [sandboxNetwork, setSandboxNetwork] = useState("");
  const [sandboxVPNTun, setSandboxVPNTun] = useState(false);
  const [blackboardMode, setBlackboardMode] = useState<BlackboardMode>(defaultBlackboardMode);
  const [preflight, setPreflight] = useState<PreflightResult | null>(null);
  const [skillsPreview, setSkillsPreview] = useState<Skill[] | null>(null);
  const [skillsPreviewLoading, setSkillsPreviewLoading] = useState(false);
  const [skillsPreviewError, setSkillsPreviewError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const [pluginData, providerData, profileData, project, health] = await Promise.all([
          apiGet<{ plugins: RuntimePlugin[] }>("/api/runtime-plugins"),
          apiGet<{ providers: ModelProvider[] }>("/api/model-providers"),
          apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles"),
          projectId ? apiGet<Project>(`/api/projects/${projectId}`) : Promise.resolve(null),
          apiGet<Health>("/api/health").catch(() => null),
        ]);
        if (cancelled) return;
        const loadedPlugins = pluginData.plugins ?? [];
        const loadedProviders = providerData.providers ?? [];
        const loadedProfiles = profileData.profiles ?? [];
        const runner = project?.defaults.runner || "sandbox";
        const state = initialLaunchState({
          plugins: loadedPlugins,
          modelProviders: loadedProviders,
          profiles: loadedProfiles,
          projectRunner: runner,
        });
        setPlugins(loadedPlugins);
        setModelProviders(loadedProviders);
        setProfiles(loadedProfiles);
        setProject(project);
        setDefaultRunner(runner);
        setForm(state.form);
        setPresetId(state.presetId);
        setPresetOpen(state.presetOpen);
        // Prefer the engine health reports as ready (daemon may rewrite
        // container_cli when only Podman is installed).
        const cli = health?.runner?.container_cli?.toLowerCase();
        const kind = health?.runner?.engine_kind?.toLowerCase();
        if (cli === "podman" || kind === "podman") {
          setContainerCLI("podman");
        } else {
          setContainerCLI("docker");
        }
        setError(null);
      } catch (cause) {
        if (!cancelled) setError((cause as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  const presetMode = presetId.trim() !== "";
  const skillsProfileId = launchProfileIdForSkillsPreview(presetId, "");
  const enabledSkillsPreview = useMemo(
    () => (skillsPreview ? enabledLaunchSkills(skillsPreview) : []),
    [skillsPreview],
  );
  const launchRuntimePlugins = useMemo(() => launchRuntimes(plugins), [plugins]);
  const runtimePresets = useMemo(() => presetsForRuntime(profiles, form.runtime), [profiles, form.runtime]);
  const selectedPlugin = useMemo(
    () => plugins.find((plugin) => plugin.id === form.runtime),
    [plugins, form.runtime],
  );
  const compatibleProviders = useMemo(
    () => selectableModelProviders(modelProviders, selectedPlugin, form.modelProviderId),
    [modelProviders, selectedPlugin, form.modelProviderId],
  );
  const selectedProvider = useMemo(
    () => compatibleProviders.find((provider) => provider.id === form.modelProviderId),
    [compatibleProviders, form.modelProviderId],
  );
  const modelOptions = useMemo(() => modelsForProvider(selectedProvider), [selectedProvider]);

  useEffect(() => {
    if (!canPreviewLaunchSkills(form, presetId)) {
      const clearTimer = window.setTimeout(() => {
        setSkillsPreview(null);
        setSkillsPreviewError(null);
        setSkillsPreviewLoading(false);
      }, 0);
      return () => window.clearTimeout(clearTimer);
    }
    let cancelled = false;
    const timer = window.setTimeout(() => {
      void (async () => {
        setSkillsPreviewLoading(true);
        setSkillsPreviewError(null);
        try {
          const data = await apiGet<{ skills: Skill[] }>(
            skillsProfileId
              ? `/api/skills?runtime_profile_id=${encodeURIComponent(skillsProfileId)}`
              : "/api/skills",
          );
          if (!cancelled) setSkillsPreview(data.skills ?? []);
        } catch (cause) {
          if (!cancelled) {
            setSkillsPreview(null);
            setSkillsPreviewError((cause as Error).message);
          }
        } finally {
          if (!cancelled) setSkillsPreviewLoading(false);
        }
      })();
    }, 250);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [form, presetId, skillsProfileId]);

  function clearPreflight() {
    setPreflight(null);
  }

  function updateRuntime(runtime: string) {
    const nextPresetId = presetMatchesRuntime(presetId, profiles, runtime) ? presetId : "";
    if (nextPresetId) {
      const preset = profiles.find((profile) => profile.id === nextPresetId);
      if (preset) setForm(formFromPreset(preset, modelProviders, defaultRunner));
    } else {
      setPresetId("");
      setForm(simpleLaunchFormForRuntime(runtime, plugins, modelProviders, defaultRunner));
    }
    clearPreflight();
  }

  function updateModelProvider(modelProviderId: string) {
    const provider = compatibleProviders.find((candidate) => candidate.id === modelProviderId);
    const nextModels = modelsForProvider(provider);
    setForm((current) => ({ ...current, modelProviderId, modelOverride: nextModels[0] ?? "" }));
    clearPreflight();
  }

  function updatePreset(nextPresetId: string) {
    if (!nextPresetId.trim()) {
      setPresetId("");
      setForm(simpleLaunchFormForRuntime(form.runtime, plugins, modelProviders, defaultRunner));
      clearPreflight();
      return;
    }
    const preset = profiles.find((profile) => profile.id === nextPresetId);
    if (!preset) return;
    setPresetId(nextPresetId);
    setForm(formFromPreset(preset, modelProviders, defaultRunner));
    clearPreflight();
  }

  function launchPayload() {
    const selection = launchSelectionPayload(presetId, form);
    return {
      ...selection,
      runner: form.runner,
      ...(form.runner === "host" ? { host_activated: hostActivated } : {}),
      run_controls: launchRunControls(hostActivated, form.runner, containerCLI, sandboxNetwork, sandboxVPNTun, blackboardMode),
    };
  }

  async function runPreflight(endpoint: string) {
    const checked = await apiPost<PreflightResult>(endpoint, launchPayload());
    setPreflight(checked);
    return checked;
  }

  function launchReady(input: string) {
    return canLaunch(input, form, { presetId }) && (presetMode || compatibleProviders.length > 0);
  }

  return {
    plugins,
    profiles,
    project,
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
    runtimePresets,
    compatibleProviders,
    modelOptions,
    error,
    setError,
    updateRuntime,
    updateModelProvider,
    updatePreset,
    launchPayload,
    runPreflight,
    launchReady,
  };
}

export type RuntimeLaunchController = ReturnType<typeof useRuntimeLaunchControls>;

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
    runtimePresets,
    compatibleProviders,
    modelOptions,
    error,
    updateRuntime,
    updateModelProvider,
    updatePreset,
  } = controller;
  useEffect(() => {
    if (!allowDisabledBlackboardMode && blackboardMode === "disabled") {
      setBlackboardMode("interactive");
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
    { mode: "interactive", icon: UserCheck, title: "Interactive", description: "You decide when Runtime work is committed to the Blackboard.", disabled: false },
    { mode: "working_graph", icon: Sparkles, title: "Working Graph", description: "The Runtime emits local intents. The Harness settles them into Blackboard in order.", disabled: false },
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

          <ConfigAccordion icon={GitBranch} title="Blackboard mode" summary={blackboardMode === "working_graph" ? "Working Graph" : blackboardMode === "disabled" ? "Disabled" : "Interactive"}>
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
                : blackboardMode === "working_graph"
                  ? "The Runtime reads Blackboard through the CLI and emits local write intents. The Harness owns versions, idempotency, and ordered settlement."
                  : "The Runtime receives full Blackboard CLI access. The operator and Runtime decide when to write."}
            </p>
          </ConfigAccordion>

          {controller.profiles.length > 0 && (
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
                  {runtimePresets.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
                </ControlSelect>
                <p className="text-xs text-muted-foreground">A Runtime Profile copies its full advanced configuration into this new {ownerLabel}. Later Profile edits do not change it.</p>
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
  const blackboardDisplay = blackboardMode === "working_graph" ? "Working Graph" : blackboardMode === "disabled" ? "Disabled" : "Interactive";
  const profileDisplay = presetMode ? `Runtime Profile: ${profiles.find((profile) => profile.id === presetId)?.name ?? presetId}` : "Direct configuration";
  const apiKeyEnv = summaryProvider?.api_key_env ?? "";
  const credentialConfigured = bindings !== null && bindings.some((binding) => binding.credential_ref === apiKeyEnv && !binding.disabled);

  return (
    <aside className="h-fit lg:sticky lg:top-6">
      <div className="rounded-lg border border-border bg-card shadow-sm">
        <div className="border-b border-border px-4 py-3"><span className="text-sm font-medium">Launch summary</span></div>
        <dl className="space-y-2.5 px-4 py-3.5 text-xs">
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Runtime</dt><dd className="min-w-0 truncate font-medium">{runtimeDisplay}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Model</dt><dd className="min-w-0 truncate font-mono">{modelDisplay}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Effort</dt><dd className="font-medium">{displayReasoningEffort(form.reasoningEffort)}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Runner</dt><dd className="font-medium">{runnerDisplay}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Blackboard</dt><dd className="font-medium">{blackboardDisplay}</dd></div>
          <div className="flex justify-between gap-3"><dt className="text-muted-foreground">Skills</dt><dd className="font-medium">{enabledSkillsPreview.length} enabled</dd></div>
        </dl>
        <div className="border-t border-border p-3.5">
          <Button type={submit ? "submit" : "button"} onClick={onClick} disabled={disabled} className="w-full">
            <Rocket className="h-4 w-4" /> {busy ? busyLabel : label}
          </Button>
          <p className="mt-2 text-center text-xs text-muted-foreground">Preflight checks run automatically before launch.</p>
        </div>
      </div>
      <div className="mt-3 rounded-lg border border-dashed border-border p-3.5 text-xs text-muted-foreground">
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

function launchUnavailableReason({
  input,
  inputLabel,
  form,
  presetMode,
  compatibleProviderCount,
  hostBlocked,
}: {
  input: string;
  inputLabel: string;
  form: Pick<LaunchForm, "runtime" | "modelProviderId">;
  presetMode: boolean;
  compatibleProviderCount: number;
  hostBlocked: boolean;
}) {
  if (hostBlocked) return "Activate the Host runner acknowledgement before launching.";
  if (!form.runtime.trim()) return "Select a runtime before launching.";
  if (!presetMode && compatibleProviderCount === 0) return "Select a compatible model provider before launching.";
  if (!presetMode && !form.modelProviderId.trim()) return "Select a model provider before launching.";
  if (!input.trim()) return `Enter ${inputLabel} before launching.`;
  return null;
}

function launchRunControls(
  hostActivated: boolean,
  runner: string,
  containerCLI: "docker" | "podman",
  sandboxNetwork: string,
  sandboxVPNTun: boolean,
  blackboardMode: BlackboardMode,
) {
  return {
    ...(runner === "host" ? { host_activated: hostActivated } : {}),
    ...(runner === "sandbox" ? { container_cli: containerCLI } : {}),
    ...(runner === "sandbox" && sandboxNetwork ? { sandbox_network: sandboxNetwork } : {}),
    ...(runner === "sandbox" && sandboxVPNTun && sandboxNetwork !== "host_proxy_only"
      ? { sandbox_vpn_tun: true }
      : {}),
    blackboard_mode: blackboardMode,
  };
}

function formatAPIKeyStatus(modelProvider: NonNullable<PreflightResult["model_provider"]>): string {
  if (modelProvider.api_key_source && modelProvider.api_key_env) return `${modelProvider.api_key_source} via ${modelProvider.api_key_env}`;
  return modelProvider.api_key_source || modelProvider.api_key_env || "not configured";
}

const SANDBOX_PREFLIGHT_CHECKS = new Set([
  "container_engines",
  "container_engine",
  "sandbox_runtime_root",
  "sandbox_vpn_tun",
]);

const PREFLIGHT_CHECK_LABELS: Record<string, string> = {
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

function preflightCheckLabel(name: string) {
  return PREFLIGHT_CHECK_LABELS[name] ?? name.replaceAll("_", " ");
}

function PreflightCheckRow({ check }: { check: PreflightResult["checks"][number] }) {
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

function PreflightCard({ preflight }: { preflight: PreflightResult }) {
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

function LaunchSkillsPreviewCard({ presetMode, profileId, loading, error, skills, ready }: { presetMode: boolean; profileId: string; loading: boolean; error: string | null; skills: Skill[]; ready: boolean }) {
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
