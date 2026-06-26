import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { BookOpen, Rocket, AlertTriangle, CheckCircle2, XCircle, ChevronRight } from "lucide-react";
import {
  apiGet,
  apiPost,
  type ModelProvider,
  type PreflightResult,
  type Project,
  type RuntimePlugin,
  type RuntimeProfile,
  type Skill,
} from "@/lib/api";
import { Button, Card, Label, Textarea, Badge, Select } from "@/components/ui";
import { BackLink, PageContainer } from "@/components/shared";
import { selectableModelProviders } from "@/pages/runtimeProfileForm";
import {
  canLaunch,
  formFromPreset,
  initialLaunchState,
  launchRuntimes,
  launchModelOverridePayload,
  launchRuntimeProfileId,
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

export function TaskLaunchPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const [plugins, setPlugins] = useState<RuntimePlugin[]>([]);
  const [modelProviders, setModelProviders] = useState<ModelProvider[]>([]);
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [projectRunner, setProjectRunner] = useState("sandbox");
  const [form, setForm] = useState<LaunchForm>({
    runtime: "",
    modelProviderId: "",
    modelOverride: "",
    runner: "sandbox",
  });
  const [presetId, setPresetId] = useState("");
  const [presetOpen, setPresetOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [goal, setGoal] = useState("");
  const [yolo, setYolo] = useState(false);
  const [hostActivated, setHostActivated] = useState(false);
  const [launching, setLaunching] = useState(false);
  const [preflight, setPreflight] = useState<PreflightResult | null>(null);
  const [resolvedProfileId, setResolvedProfileId] = useState("");
  const [skillsPreview, setSkillsPreview] = useState<Skill[] | null>(null);
  const [skillsPreviewLoading, setSkillsPreviewLoading] = useState(false);
  const [skillsPreviewError, setSkillsPreviewError] = useState<string | null>(null);

  const presetMode = presetId.trim() !== "";
  const skillsProfileId = launchProfileIdForSkillsPreview(presetId, resolvedProfileId);
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
    (async () => {
      try {
        if (!projectId) return;
        const [pluginData, providerData, profileData, project] = await Promise.all([
          apiGet<{ plugins: RuntimePlugin[] }>("/api/runtime-plugins"),
          apiGet<{ providers: ModelProvider[] }>("/api/model-providers"),
          apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles"),
          apiGet<Project>(`/api/projects/${projectId}`),
        ]);
        const loadedPlugins = pluginData.plugins ?? [];
        const loadedProviders = providerData.providers ?? [];
        const loadedProfiles = profileData.profiles ?? [];
        setPlugins(loadedPlugins);
        setModelProviders(loadedProviders);
        setProfiles(loadedProfiles);
        setProjectRunner(project.defaults.runner || "sandbox");
        const state = initialLaunchState({
          plugins: loadedPlugins,
          modelProviders: loadedProviders,
          profiles: loadedProfiles,
          defaultRuntimeProfileId: project.defaults.runtime_profile,
          projectRunner: project.defaults.runner,
        });
        setForm(state.form);
        setPresetId(state.presetId);
        setPresetOpen(state.presetOpen);
      } catch (e) {
        setError((e as Error).message);
      }
    })();
  }, [projectId]);

  useEffect(() => {
    if (!canPreviewLaunchSkills(form, presetId)) {
      setResolvedProfileId("");
      setSkillsPreview(null);
      setSkillsPreviewError(null);
      setSkillsPreviewLoading(false);
      return;
    }

    let cancelled = false;
    const timer = window.setTimeout(() => {
      void (async () => {
        setSkillsPreviewLoading(true);
        setSkillsPreviewError(null);
        try {
          let profileId = presetId.trim();
          if (!profileId) {
            const resolved = await apiPost<{
              profile_id: string;
              profile: RuntimeProfile;
            }>("/api/runtime-profiles/resolve-launch", resolveLaunchPayload(form));
            if (cancelled) return;
            profileId = resolved.profile_id;
            setResolvedProfileId(profileId);
          } else if (!cancelled) {
            setResolvedProfileId("");
          }

          const data = await apiGet<{ skills: Skill[] }>(
            `/api/skills?runtime_profile_id=${encodeURIComponent(profileId)}`,
          );
          if (cancelled) return;
          setSkillsPreview(data.skills ?? []);
        } catch (e) {
          if (cancelled) return;
          setSkillsPreview(null);
          setSkillsPreviewError((e as Error).message);
        } finally {
          if (!cancelled) setSkillsPreviewLoading(false);
        }
      })();
    }, 250);

    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [presetId, form.runtime, form.modelProviderId, form.modelOverride]);

  function updateRuntime(runtime: string) {
    const nextPresetId = presetMatchesRuntime(presetId, profiles, runtime) ? presetId : "";
    if (nextPresetId) {
      const preset = profiles.find((profile) => profile.id === nextPresetId);
      if (preset) {
        setForm(formFromPreset(preset, modelProviders, projectRunner));
        setPresetId(nextPresetId);
      }
    } else {
      setPresetId("");
      setForm(simpleLaunchFormForRuntime(runtime, plugins, modelProviders, projectRunner));
    }
    setPreflight(null);
  }

  function updateModelProvider(modelProviderId: string) {
    const provider = compatibleProviders.find((candidate) => candidate.id === modelProviderId);
    const nextModels = modelsForProvider(provider);
    setForm((current) => ({
      ...current,
      modelProviderId,
      modelOverride: nextModels[0] ?? "",
    }));
    setPreflight(null);
  }

  function updatePreset(nextPresetId: string) {
    if (!nextPresetId.trim()) {
      setPresetId("");
      setForm(simpleLaunchFormForRuntime(form.runtime, plugins, modelProviders, projectRunner));
      setPreflight(null);
      return;
    }
    const preset = profiles.find((profile) => profile.id === nextPresetId);
    if (!preset) return;
    setPresetId(nextPresetId);
    setForm(formFromPreset(preset, modelProviders, projectRunner));
    setPreflight(null);
  }

  async function launch() {
    if (!projectId) return;
    setLaunching(true);
    setError(null);
    try {
      let profileId = presetId.trim();
      if (!profileId) {
        const resolved = await apiPost<{
          profile_id: string;
          profile: RuntimeProfile;
          created: boolean;
        }>("/api/runtime-profiles/resolve-launch", resolveLaunchPayload(form));
        profileId = resolved.profile_id;
      }
      profileId = launchRuntimeProfileId(presetId, profileId);

      const launchOverride = launchModelOverridePayload(presetId, form);
      const checked = await apiPost<PreflightResult>(`/api/projects/${projectId}/preflight`, {
        runtime_profile_id: profileId,
        runner: form.runner,
        run_controls: { yolo, host_activated: hostActivated },
        ...launchOverride,
      });
      setPreflight(checked);
      if (!checked.pass) {
        setError("preflight failed");
        return;
      }
      const created = await apiPost<{ id: string }>(`/api/projects/${projectId}/tasks`, {
        goal,
        runtime_profile_id: profileId,
        runner: form.runner,
        run_controls: { yolo, host_activated: hostActivated },
        ...launchOverride,
      });
      navigate(`/projects/${projectId}/tasks/${created.id}`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLaunching(false);
    }
  }

  const hostRunner = form.runner === "host";
  const hostWithYolo = hostRunner || yolo;
  const hostBlocked = hostRunner && !hostActivated && !yolo;
  const launchReady = canLaunch(goal, form) && compatibleProviders.length > 0;

  return (
    <PageContainer className="max-w-2xl">
      <BackLink to={`/projects/${projectId}`}>Back to dashboard</BackLink>
      <h2 className="text-xl font-semibold mb-6">Launch task</h2>

      <div className="space-y-4">
        <div>
          <Label htmlFor="goal">Task goal</Label>
          <Textarea id="goal" value={goal} onChange={(e) => setGoal(e.target.value)} placeholder="Enumerate example.com and assess exposure" />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <Label htmlFor="launch-runtime">Runtime</Label>
            <Select
              id="launch-runtime"
              value={form.runtime}
              disabled={presetMode}
              onChange={(e) => updateRuntime(e.target.value)}
            >
              {launchRuntimePlugins.map((plugin) => (
                <option key={plugin.id} value={plugin.id}>
                  {plugin.name}
                </option>
              ))}
            </Select>
          </div>
          <div>
            <Label htmlFor="launch-runner">Runner</Label>
            <Select
              id="launch-runner"
              value={form.runner}
              onChange={(e) => {
                setForm((current) => ({ ...current, runner: e.target.value }));
                setPreflight(null);
              }}
            >
              <option value="sandbox">sandbox</option>
              <option value="host">host</option>
            </Select>
          </div>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <Label htmlFor="launch-model-provider">Model provider</Label>
            <Select
              id="launch-model-provider"
              value={form.modelProviderId}
              disabled={presetMode}
              onChange={(e) => updateModelProvider(e.target.value)}
            >
              {compatibleProviders.length === 0 ? (
                <option value="">No compatible providers</option>
              ) : (
                compatibleProviders.map((provider) => (
                  <option key={provider.id} value={provider.id}>
                    {provider.name}
                  </option>
                ))
              )}
            </Select>
          </div>
          <div>
            <Label htmlFor="launch-model">Model</Label>
            <Select
              id="launch-model"
              value={form.modelOverride}
              onChange={(e) => {
                setForm((current) => ({ ...current, modelOverride: e.target.value }));
                setPreflight(null);
              }}
              disabled={!presetMode && modelOptions.length === 0}
            >
              {modelOptions.length === 0 ? (
                <option value="">{form.modelOverride || "Default model"}</option>
              ) : (
                <>
                  {form.modelOverride && !modelOptions.includes(form.modelOverride) && (
                    <option value={form.modelOverride}>{form.modelOverride}</option>
                  )}
                  {modelOptions.map((model) => (
                    <option key={model} value={model}>
                      {model}
                    </option>
                  ))}
                </>
              )}
            </Select>
          </div>
        </div>

        {profiles.length > 0 && (
          <Card className="border-border/70 bg-muted/10 p-3">
            <button
              type="button"
              onClick={() => setPresetOpen((open) => !open)}
              className="flex w-full items-center gap-2 text-left text-sm font-medium"
            >
              <ChevronRight className={`size-4 shrink-0 transition-transform ${presetOpen ? "rotate-90" : ""}`} />
              Use saved preset
            </button>
            {presetOpen && (
              <div className="mt-3 space-y-2">
                <Label htmlFor="launch-preset">Runtime profile preset</Label>
                <Select id="launch-preset" value={presetId} onChange={(e) => updatePreset(e.target.value)}>
                  <option value="">Auto-resolve minimal profile</option>
                  {runtimePresets.map((profile) => (
                    <option key={profile.id} value={profile.id}>
                      {profile.name}
                    </option>
                  ))}
                </Select>
                <p className="text-xs text-muted-foreground">
                  Presets carry MCP, skills, and extension configuration. Runtime and model provider lock while a preset is selected.
                </p>
              </div>
            )}
          </Card>
        )}

        {/* Safety states must be visually loud (prd.md:183). */}
        {canPreviewLaunchSkills(form, presetId) && (
          <LaunchSkillsPreviewCard
            presetMode={presetMode}
            profileId={skillsProfileId}
            loading={skillsPreviewLoading}
            error={skillsPreviewError}
            skills={enabledSkillsPreview}
            ready={skillsPreview !== null}
          />
        )}

        {hostWithYolo && (
          <Card className="border-warning bg-warning/10 p-3 space-y-2">
            <div className="flex items-center gap-2 text-warning">
              <AlertTriangle className="h-4 w-4" />
              <span className="text-sm font-medium">
                {hostRunner && yolo ? "HOST runner + YOLO mode" : hostRunner ? "HOST runner — runs on your machine" : "YOLO mode — approvals bypassed"}
              </span>
            </div>
            {hostRunner && !yolo && (
              <label className="flex items-start gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={hostActivated}
                  onChange={(e) => {
                    setHostActivated(e.target.checked);
                    setPreflight(null);
                  }}
                  className="mt-0.5 h-4 w-4 accent-warning"
                />
                <span>
                  I explicitly activate the host runner for this task. Commands execute on this machine outside the sandbox.
                </span>
              </label>
            )}
          </Card>
        )}

        {preflight && (
          <Card className={preflight.pass ? "border-emerald-500/40 bg-emerald-500/5 p-3" : "border-destructive/40 bg-destructive/5 p-3"}>
            <div className="space-y-2">
              {preflight.checks.map((check) => (
                <div key={check.name} className="flex items-start gap-2 text-sm">
                  {check.status === "pass" ? (
                    <CheckCircle2 className="mt-0.5 h-4 w-4 text-emerald-400" />
                  ) : (
                    <XCircle className="mt-0.5 h-4 w-4 text-destructive" />
                  )}
                  <div>
                    <span className="font-medium">{check.name}</span>
                    {check.detail && <span className="text-muted-foreground">: {check.detail}</span>}
                  </div>
                </div>
              ))}
            </div>
            {preflight.model_provider && (
              <div className="mt-3 border-t border-border/60 pt-3">
                <p className="mb-2 text-sm font-medium">Model provider</p>
                <div className="rounded-lg border border-border/60 bg-background/50 p-2 text-sm space-y-1">
                  <div className="font-medium">{preflight.model_provider.model_provider_name || preflight.model_provider.model_provider_id}</div>
                  <div className="font-mono text-xs text-muted-foreground">
                    {preflight.model_provider.model} via {preflight.model_provider.protocol}
                  </div>
                  <div className="text-xs text-muted-foreground">{preflight.model_provider.base_url}</div>
                  <div className="font-mono text-xs text-muted-foreground">API key: {preflight.model_provider.api_key_env}</div>
                </div>
              </div>
            )}
            {preflight.runtime_extensions && preflight.runtime_extensions.length > 0 && (
              <div className="mt-3 border-t border-border/60 pt-3">
                <p className="mb-2 text-sm font-medium">Runtime extensions</p>
                <div className="space-y-2">
                  {preflight.runtime_extensions.map((extension) => (
                    <div key={extension.id} className="rounded-lg border border-border/60 bg-background/50 p-2 text-sm">
                      <div className="font-medium">{extension.name || extension.id}</div>
                      <div className="font-mono text-xs text-muted-foreground">{extension.id}</div>
                      {extension.source === "catalog" && extension.install_ref && (
                        <div className="text-xs text-muted-foreground">Install: {extension.install_ref}</div>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            )}
            {preflight.skills && preflight.skills.length > 0 && (
              <div className="mt-3 border-t border-border/60 pt-3">
                <p className="mb-2 text-sm font-medium">Enabled Skills</p>
                <div className="space-y-2">
                  {preflight.skills.map((skill) => (
                    <div key={skill.id} className="rounded-lg border border-border/60 bg-background/50 p-2 text-sm">
                      <div className="font-medium">{skill.name || skill.id}</div>
                      <div className="font-mono text-xs text-muted-foreground">{skill.id}</div>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </Card>
        )}

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={yolo}
            onChange={(e) => setYolo(e.target.checked)}
            className="h-4 w-4 accent-warning"
          />
          <span className={yolo ? "text-warning font-medium" : ""}>YOLO mode (skip per-action approvals)</span>
          {yolo && <Badge variant="warning">loud</Badge>}
        </label>

        {error && <p className="text-sm text-destructive">{error}</p>}

        <Button onClick={launch} disabled={!launchReady || launching || hostBlocked}>
          <Rocket className="h-4 w-4 mr-1" /> {launching ? "Launching…" : "Launch"}
        </Button>
      </div>
    </PageContainer>
  );
}

function LaunchSkillsPreviewCard({
  presetMode,
  profileId,
  loading,
  error,
  skills,
  ready,
}: {
  presetMode: boolean;
  profileId: string;
  loading: boolean;
  error: string | null;
  skills: Skill[];
  ready: boolean;
}) {
  return (
    <Card className="border-border/70 bg-muted/10 p-3">
      <div className="mb-2 flex items-center gap-2">
        <BookOpen className="h-4 w-4 text-muted-foreground" />
        <p className="text-sm font-medium">Skills for this launch</p>
      </div>
      <p className="mb-3 text-xs text-muted-foreground">{launchSkillsPreviewDetail(presetMode)}</p>
      {profileId && (
        <p className="mb-3 font-mono text-[11px] text-muted-foreground truncate">Profile: {profileId}</p>
      )}
      {loading && <p className="text-sm text-muted-foreground">Loading enabled skills…</p>}
      {error && <p className="text-sm text-destructive">{error}</p>}
      {!loading && !error && ready && skills.length === 0 && (
        <p className="text-sm text-muted-foreground">No skills enabled for this profile.</p>
      )}
      {!loading && !error && skills.length > 0 && (
        <div className="space-y-2">
          {skills.map((skill) => (
            <div key={skill.id} className="rounded-lg border border-border/60 bg-background/50 p-2 text-sm">
              <div className="font-medium">{skill.name || skill.id}</div>
              <div className="font-mono text-xs text-muted-foreground">{skill.id}</div>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}