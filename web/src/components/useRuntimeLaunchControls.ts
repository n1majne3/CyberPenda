import { useEffect, useMemo, useState } from "react";
import { apiGet, apiPost, type BlackboardMode, type Health, type ModelProvider, type PreflightResult, type Project, type RuntimePlugin, type RuntimeProfile, type Skill } from "@/lib/api";
import { selectableModelProviders } from "@/pages/runtimeProfileForm";
import { canLaunch, formFromPreset, initialLaunchState, launchSelectionPayload, launchRuntimes, launchableProfiles, modelsForProvider, presetMatchesRuntime, simpleLaunchFormForRuntime, type LaunchForm } from "@/pages/taskLaunchForm";
import { canPreviewLaunchSkills, enabledLaunchSkills, launchProfileIdForSkillsPreview } from "@/pages/taskLaunchSkills";

export type RuntimeLaunchControlsOptions = {
  projectId?: string;
  defaultBlackboardMode?: BlackboardMode;
};

// The hook and its render surface intentionally live together so Task and
// Session launch behavior cannot drift behind a component-only abstraction.
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
  const [hostBrowserChoice, setHostBrowserChoice] = useState({ projectId, enabled: false });
  if (hostBrowserChoice.projectId !== projectId) {
    setHostBrowserChoice({ projectId, enabled: false });
  }
  const hostBrowserEnabled = hostBrowserChoice.projectId === projectId && hostBrowserChoice.enabled;
  const setHostBrowserEnabled = (enabled: boolean) => setHostBrowserChoice({ projectId, enabled });
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
          apiGet<Health>("/health").catch(() => null),
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
  // Every launchable Runtime Profile stays visible; the current Runtime
  // selection does not filter the list.
  const launchProfiles = useMemo(() => launchableProfiles(profiles), [profiles]);
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
      host_browser_enabled: hostBrowserEnabled,
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
    hostBrowserEnabled,
    setHostBrowserEnabled,
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

export function launchUnavailableReason({
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

export function launchRunControls(
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
