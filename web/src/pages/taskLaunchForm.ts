import type { ModelProvider, RuntimePlugin, RuntimeProfile } from "@/lib/api";
import { selectableModelProviders } from "@/pages/runtimeProfileForm";

export const LAUNCH_RUNTIME_IDS = ["codex", "claude_code", "pi"] as const;

export type LaunchForm = {
  runtime: string;
  modelProviderId: string;
  modelOverride: string;
  runner: string;
};

export function launchRuntimes(plugins: RuntimePlugin[]): RuntimePlugin[] {
  const allowed = new Set<string>(LAUNCH_RUNTIME_IDS);
  return plugins.filter((plugin) => allowed.has(plugin.id));
}

export function canLaunch(
  goal: string,
  form: Pick<LaunchForm, "runtime" | "modelProviderId">,
): boolean {
  return goal.trim() !== "" && form.runtime.trim() !== "" && form.modelProviderId.trim() !== "";
}

export function modelsForProvider(provider: ModelProvider | undefined): string[] {
  if (!provider) return [];
  const manual = provider.catalog?.manual ?? [];
  const refreshed = provider.catalog?.refreshed ?? [];
  const defaultModel = provider.catalog?.default_model?.trim();
  const models = [...new Set([...manual, ...refreshed].map((model) => model.trim()).filter(Boolean))];
  if (defaultModel && !models.includes(defaultModel)) {
    models.unshift(defaultModel);
  }
  if (models.length === 0 && defaultModel) {
    return [defaultModel];
  }
  return models;
}

export function launchSelectionFromProfile(profile: RuntimeProfile): Partial<LaunchForm> {
  return {
    runtime: profile.provider,
    modelProviderId: profile.fields.model_provider_id?.trim() ?? "",
    modelOverride: profile.fields.model_override?.trim() ?? "",
    runner: profile.fields.default_runner?.trim() || "sandbox",
  };
}

export function resolveLaunchPayload(
  form: Pick<LaunchForm, "runtime" | "modelProviderId" | "modelOverride">,
): {
  provider: string;
  model_provider_id: string;
  model_override?: string;
} {
  const payload: {
    provider: string;
    model_provider_id: string;
    model_override?: string;
  } = {
    provider: form.runtime,
    model_provider_id: form.modelProviderId,
  };
  const modelOverride = form.modelOverride.trim();
  if (modelOverride) {
    payload.model_override = modelOverride;
  }
  return payload;
}

type DefaultLaunchFormInput = {
  plugins: RuntimePlugin[];
  modelProviders: ModelProvider[];
  defaultProfile?: RuntimeProfile;
  projectRunner?: string;
};

export function presetsForRuntime(profiles: RuntimeProfile[], runtime: string): RuntimeProfile[] {
  const normalized = runtime.trim();
  if (!normalized) return [];
  return profiles.filter((profile) => profile.provider === normalized);
}

export function presetMatchesRuntime(
  presetId: string,
  profiles: RuntimeProfile[],
  runtime: string,
): boolean {
  const normalized = presetId.trim();
  if (!normalized) return true;
  const preset = profiles.find((profile) => profile.id === normalized);
  return preset?.provider === runtime.trim();
}

export function defaultPresetSelection(
  profiles: RuntimeProfile[],
  defaultRuntimeProfileId?: string,
): string {
  const normalized = defaultRuntimeProfileId?.trim();
  if (!normalized) return "";
  return profiles.some((profile) => profile.id === normalized) ? normalized : "";
}

export function formFromPreset(
  profile: RuntimeProfile,
  modelProviders: ModelProvider[],
  projectRunner?: string,
): LaunchForm {
  const selection = launchSelectionFromProfile(profile);
  const provider = modelProviders.find((candidate) => candidate.id === selection.modelProviderId);
  const models = modelsForProvider(provider);
  const modelOverride =
    selection.modelOverride ||
    models[0] ||
    provider?.catalog?.default_model?.trim() ||
    "";
  return {
    runtime: selection.runtime ?? "",
    modelProviderId: selection.modelProviderId ?? "",
    modelOverride,
    runner: projectRunner?.trim() || selection.runner || "sandbox",
  };
}

export function launchRuntimeProfileId(presetId: string, resolvedProfileId: string): string {
  return presetId.trim() || resolvedProfileId;
}

export function launchModelOverridePayload(
  presetId: string,
  form: Pick<LaunchForm, "modelOverride">,
): { model_override?: string } {
  if (!presetId.trim()) {
    return {};
  }
  const modelOverride = form.modelOverride.trim();
  if (!modelOverride) {
    return {};
  }
  return { model_override: modelOverride };
}

type InitialLaunchStateInput = {
  plugins: RuntimePlugin[];
  modelProviders: ModelProvider[];
  profiles: RuntimeProfile[];
  defaultRuntimeProfileId?: string;
  projectRunner?: string;
};

export function simpleLaunchFormForRuntime(
  runtime: string,
  plugins: RuntimePlugin[],
  modelProviders: ModelProvider[],
  projectRunner?: string,
): LaunchForm {
  const plugin = plugins.find((candidate) => candidate.id === runtime);
  const compatible = selectableModelProviders(modelProviders, plugin);
  const provider = compatible[0];
  const models = modelsForProvider(provider);
  return {
    runtime,
    modelProviderId: provider?.id ?? "",
    modelOverride: models[0] ?? "",
    runner: projectRunner?.trim() || "sandbox",
  };
}

export function initialLaunchState(input: InitialLaunchStateInput): {
  form: LaunchForm;
  presetId: string;
  presetOpen: boolean;
} {
  const presetId = defaultPresetSelection(input.profiles, input.defaultRuntimeProfileId);
  if (presetId) {
    const preset = input.profiles.find((profile) => profile.id === presetId);
    if (preset) {
      return {
        form: formFromPreset(preset, input.modelProviders, input.projectRunner),
        presetId,
        presetOpen: true,
      };
    }
  }
  return {
    form: defaultLaunchForm({
      plugins: input.plugins,
      modelProviders: input.modelProviders,
      projectRunner: input.projectRunner,
    }),
    presetId: "",
    presetOpen: false,
  };
}

export function defaultLaunchForm(input: DefaultLaunchFormInput): LaunchForm {
  const runtimes = launchRuntimes(input.plugins);
  const fromProfile = input.defaultProfile ? launchSelectionFromProfile(input.defaultProfile) : {};
  const runtime =
    fromProfile.runtime && runtimes.some((plugin) => plugin.id === fromProfile.runtime)
      ? fromProfile.runtime
      : runtimes[0]?.id ?? "";
  const plugin = input.plugins.find((candidate) => candidate.id === runtime);
  const compatible = selectableModelProviders(input.modelProviders, plugin);
  const modelProviderId =
    fromProfile.modelProviderId && compatible.some((provider) => provider.id === fromProfile.modelProviderId)
      ? fromProfile.modelProviderId
      : compatible[0]?.id ?? "";
  const provider = compatible.find((candidate) => candidate.id === modelProviderId);
  const models = modelsForProvider(provider);
  const modelOverride =
    fromProfile.modelOverride && models.includes(fromProfile.modelOverride)
      ? fromProfile.modelOverride
      : models[0] ?? "";
  return {
    runtime,
    modelProviderId,
    modelOverride,
    runner: input.projectRunner?.trim() || fromProfile.runner || "sandbox",
  };
}