import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, BookOpen, CheckCircle2, ChevronRight, XCircle } from "lucide-react";
import {
  apiGet,
  apiPost,
  type BlackboardConclusionMode,
  type Health,
  type ModelProvider,
  type PreflightResult,
  type Project,
  type RuntimePlugin,
  type RuntimeProfile,
  type Skill,
} from "@/lib/api";
import { Card, Label, Select } from "@/components/ui";
import { displayReasoningEffort, REASONING_EFFORT_VALUES, selectableModelProviders } from "@/pages/runtimeProfileForm";
import {
  canLaunch,
  findLaunchProfileForSelection,
  formFromPreset,
  initialLaunchState,
  launchModelOverridePayload,
  launchReasoningEffortPayload,
  launchRuntimes,
  modelsForProvider,
  presetMatchesRuntime,
  presetsForRuntime,
  resolveLaunchPayload,
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
};

// The hook and its render surface intentionally live together so Task and
// Session launch behavior cannot drift behind a component-only abstraction.
// eslint-disable-next-line react-refresh/only-export-components
export function useRuntimeLaunchControls({ projectId }: RuntimeLaunchControlsOptions = {}) {
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
  const [blackboardConclusionMode, setBlackboardConclusionMode] = useState<BlackboardConclusionMode>("interactive");
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
          defaultRuntimeProfileId: project?.defaults.runtime_profile,
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
  const matchingLaunchProfile = useMemo(
    () => findLaunchProfileForSelection(profiles, form),
    [profiles, form],
  );
  const skillsProfileId = launchProfileIdForSkillsPreview(presetId, matchingLaunchProfile?.id ?? "");
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
  const assistedConclusionSupported = selectedPlugin?.capabilities.assisted_conclusion === true;
  const assistedConclusionUnavailableReason = `${selectedPlugin?.name ?? "Selected Runtime"} does not expose the complete persistent Turn, normalized Tool/Turn event, and closed AttemptResult contract required by assisted conclusions.`;

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
          if (!skillsProfileId) {
            if (!cancelled) setSkillsPreview([]);
            return;
          }
          const data = await apiGet<{ skills: Skill[] }>(
            `/api/skills?runtime_profile_id=${encodeURIComponent(skillsProfileId)}`,
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

  function launchPayload(profileId: string) {
    return {
      runtime_profile_id: profileId,
      runner: form.runner,
      ...(form.runner === "host" ? { host_activated: hostActivated } : {}),
      ...launchModelOverridePayload(presetId, form),
      ...launchReasoningEffortPayload(form),
      run_controls: launchRunControls(hostActivated, form.runner, containerCLI, sandboxNetwork, sandboxVPNTun, blackboardConclusionMode),
    };
  }

  async function resolveRuntimeProfileId() {
    if (presetId.trim()) return presetId.trim();
    const resolved = await apiPost<{ profile_id: string }>(
      "/api/runtime-profiles/resolve-launch",
      resolveLaunchPayload(form),
    );
    return resolved.profile_id;
  }

  async function runPreflight(endpoint: string, profileId: string) {
    const checked = await apiPost<PreflightResult>(endpoint, launchPayload(profileId));
    setPreflight(checked);
    return checked;
  }

  function launchReady(input: string) {
    return canLaunch(input, form, { presetId }) &&
      (presetMode || compatibleProviders.length > 0) &&
      !(blackboardConclusionMode === "assisted" && !assistedConclusionSupported);
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
    blackboardConclusionMode,
    setBlackboardConclusionMode,
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
    assistedConclusionSupported,
    assistedConclusionUnavailableReason,
    error,
    setError,
    updateRuntime,
    updateModelProvider,
    updatePreset,
    launchPayload,
    resolveRuntimeProfileId,
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
    blackboardConclusionMode,
    setBlackboardConclusionMode,
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
    assistedConclusionSupported,
    assistedConclusionUnavailableReason,
    error,
    updateRuntime,
    updateModelProvider,
    updatePreset,
  } = controller;
  useEffect(() => {
    if (!allowDisabledBlackboardMode && blackboardConclusionMode === "disabled") {
      setBlackboardConclusionMode("interactive");
      setPreflight(null);
    }
  }, [allowDisabledBlackboardMode, blackboardConclusionMode, setBlackboardConclusionMode, setPreflight]);
  const hostRunner = form.runner === "host";
  const hostBlocked = hostRunner && !hostActivated;
  const assistedConclusionUnsupported = blackboardConclusionMode === "assisted" && !assistedConclusionSupported;
  const unavailableReason = assistedConclusionUnsupported
    ? assistedConclusionUnavailableReason
    : launchUnavailableReason({
        input: initialInput,
        inputLabel: ownerLabel === "task" ? "Task goal" : "initial input",
        form,
        presetMode,
        compatibleProviderCount: compatibleProviders.length,
        hostBlocked,
      });
  // UI value maps Docker/Podman (container engines) + Host; backend still uses runner=sandbox|host.
  const runnerSelectValue = form.runner === "host" ? "host" : containerCLI;
  const engineLabel = form.runner === "host" ? "host" : containerCLI;

  return (
    <>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <Label htmlFor="launch-runtime">Runtime</Label>
          <Select id="launch-runtime" name="runtime" value={form.runtime} disabled={presetMode} onChange={(event) => updateRuntime(event.target.value)}>
            {launchRuntimePlugins.map((plugin) => <option key={plugin.id} value={plugin.id}>{plugin.name}</option>)}
          </Select>
        </div>
        <div>
          <Label htmlFor="launch-runner">Runner</Label>
          <Select
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
          </Select>
          {form.runner === "sandbox" && (
            <p className="mt-1 text-xs text-muted-foreground">
              Container engine <span className="font-mono">{engineLabel}</span> runs the isolated task workdir.
            </p>
          )}
        </div>
      </div>

      {form.runner === "sandbox" && (
        <div className="space-y-3">
          <div>
            <Label htmlFor="launch-sandbox-network">{engineLabel === "podman" ? "Podman network" : "Docker network"}</Label>
            <Select
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
            </Select>
          </div>
          <label className="flex items-start gap-2 text-sm">
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
        </div>
      )}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <Label htmlFor="launch-model-provider">Model provider</Label>
          <Select id="launch-model-provider" name="model_provider" value={form.modelProviderId} disabled={presetMode} onChange={(event) => updateModelProvider(event.target.value)}>
            {compatibleProviders.length === 0
              ? <option value="">No compatible providers</option>
              : compatibleProviders.map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
          </Select>
        </div>
        <div>
          <Label htmlFor="launch-model">Model</Label>
          <Select
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
          </Select>
        </div>
        <div>
          <Label htmlFor="launch-reasoning-effort">Reasoning effort</Label>
          <Select id="launch-reasoning-effort" name="reasoning_effort" value={displayReasoningEffort(form.reasoningEffort)} onChange={(event) => { setForm((current) => ({ ...current, reasoningEffort: event.target.value })); setPreflight(null); }}>
            {REASONING_EFFORT_VALUES.map((effort) => <option key={effort} value={effort}>{effort}</option>)}
          </Select>
        </div>
        <div>
          <Label htmlFor="launch-blackboard-mode">Blackboard Mode</Label>
          <Select id="launch-blackboard-mode" name="blackboard_conclusion_mode" value={blackboardConclusionMode} onChange={(event) => { setBlackboardConclusionMode(event.target.value as BlackboardConclusionMode); setPreflight(null); }}>
            <option value="interactive">Interactive</option>
            <option value="assisted" disabled={!assistedConclusionSupported}>Assisted</option>
            {allowDisabledBlackboardMode && <option value="disabled">Disabled</option>}
          </Select>
          <p className="mt-1 text-xs text-muted-foreground">
            {blackboardConclusionMode === "disabled"
              ? "The Runtime does not receive Blackboard state or Blackboard access. All non-Blackboard launch context remains available."
              : blackboardConclusionMode === "assisted"
              ? "After tool-producing work, the Harness runs a bounded Conclude Turn and applies its validated Attempt result to the Blackboard."
              : assistedConclusionSupported
                ? "The operator decides when Runtime work is written to the Blackboard."
                : `${assistedConclusionUnavailableReason} ${allowDisabledBlackboardMode ? "Interactive and Disabled launch remain available." : "Interactive launch remains available."}`}
          </p>
        </div>
      </div>

      {controller.profiles.length > 0 && (
        <Card className="border-border/70 bg-muted/10 p-3">
          <button type="button" onClick={() => setPresetOpen((open) => !open)} aria-expanded={presetOpen} className="flex w-full items-center gap-2 rounded-md text-left text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
            <ChevronRight className={`size-4 shrink-0 transition-transform ${presetOpen ? "rotate-90" : ""}`} />
            Use saved preset
          </button>
          {presetOpen && (
            <div className="mt-3 space-y-2">
              <Label htmlFor="launch-preset">Runtime profile preset</Label>
              <Select id="launch-preset" name="runtime_profile_preset" value={presetId} onChange={(event) => updatePreset(event.target.value)}>
                <option value="">Auto-resolve minimal profile</option>
                {runtimePresets.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
              </Select>
              <p className="text-xs text-muted-foreground">Presets carry MCP, skills, and extension configuration. Runtime and model provider lock while a preset is selected.</p>
            </div>
          )}
        </Card>
      )}

      {canPreviewLaunchSkills(form, presetId) && (
        <LaunchSkillsPreviewCard presetMode={presetMode} profileId={skillsProfileId} loading={skillsPreviewLoading} error={skillsPreviewError} skills={enabledSkillsPreview} ready={skillsPreview !== null} />
      )}

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
  blackboardConclusionMode: BlackboardConclusionMode,
) {
  return {
    ...(runner === "host" ? { host_activated: hostActivated } : {}),
    ...(runner === "sandbox" ? { container_cli: containerCLI } : {}),
    ...(runner === "sandbox" && sandboxNetwork ? { sandbox_network: sandboxNetwork } : {}),
    ...(runner === "sandbox" && sandboxVPNTun && sandboxNetwork !== "host_proxy_only"
      ? { sandbox_vpn_tun: true }
      : {}),
    blackboard_conclusion_mode: blackboardConclusionMode,
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
    <Card className="border-border/70 bg-muted/10 p-3">
      <div className="mb-2 flex items-center gap-2"><BookOpen className="h-4 w-4 text-muted-foreground" /><p className="text-sm font-medium">Skills for this launch</p></div>
      <p className="mb-3 text-xs text-muted-foreground">{launchSkillsPreviewDetail(presetMode)}</p>
      {profileId && <p className="mb-3 font-mono text-[11px] text-muted-foreground truncate">Profile: {profileId}</p>}
      {loading && <p className="text-sm text-muted-foreground">Loading enabled skills…</p>}
      {error && <p className="text-sm text-destructive">{error}</p>}
      {!loading && !error && ready && skills.length === 0 && <p className="text-sm text-muted-foreground">{profileId ? "No skills enabled for this profile." : "No matching skills profile yet."}</p>}
      {!loading && !error && skills.length > 0 && <div className="max-h-60 space-y-2 overflow-y-auto overscroll-y-contain pr-1" aria-label={`${skills.length} enabled skills`}>{skills.map((skill) => <div key={skill.id} className="rounded-lg border border-border/60 bg-background/50 p-2 text-sm"><div className="font-medium">{skill.name || skill.id}</div><div className="font-mono text-xs text-muted-foreground">{skill.id}</div></div>)}</div>}
    </Card>
  );
}
