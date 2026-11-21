import { useEffect, useState } from "react";
import type { ModelProvider, RuntimePlugin, RuntimeProfile } from "@/lib/api";
import type { RuntimeOwnerView } from "@/lib/runtimeOwner/types";
import { displayReasoningEffort, selectableModelProviders } from "./runtimeProfileForm";
import { modelsForProvider } from "./taskLaunchForm";

export function useContinuationSelection({
  owner,
  profiles,
  modelProviders,
  runtimePlugins,
}: {
  owner: RuntimeOwnerView | null;
  profiles: RuntimeProfile[];
  modelProviders: ModelProvider[];
  runtimePlugins: RuntimePlugin[];
}) {
  const [continuationModelProvider, setContinuationModelProvider] = useState("");
  const [continuationModelOverride, setContinuationModelOverride] = useState("");
  const [continuationReasoningEffort, setContinuationReasoningEffort] = useState("high");
  const [turnSelectionSeeded, setTurnSelectionSeeded] = useState(false);

  // Seed the composer once from the preceding Runtime Turn Selection. Later
  // submits retain the operator's choice instead of resetting.
  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    if (turnSelectionSeeded || !owner) return;
    const preceding = owner.runtimeControls?.turn_selection;
    if (!preceding) {
      setTurnSelectionSeeded(true);
      return;
    }
    setContinuationModelProvider(preceding.model_provider_id?.trim() ?? "");
    setContinuationModelOverride(preceding.model?.trim() ?? "");
    setContinuationReasoningEffort(displayReasoningEffort(preceding.reasoning_effort));
    setTurnSelectionSeeded(true);
  }, [owner, turnSelectionSeeded]);
  /* eslint-enable react-hooks/set-state-in-effect */

  const currentProfileRuntimeProvider = owner?.runtimeConfiguration?.runtime_plugin_id ?? profiles.find((profile) => profile.id === owner?.runtimeProfileID)?.provider;
  const currentRuntimeProvider =
    owner?.runtimeControls?.runtime_provider ??
    owner?.activeContinuation?.runtimeProvider ??
    owner?.latestContinuation?.runtimeProvider ??
    currentProfileRuntimeProvider;
  const runtimePlugin = runtimePlugins.find((plugin) => plugin.id === currentRuntimeProvider);
  const continuationModelProviders = selectableModelProviders(
    modelProviders,
    runtimePlugin,
    continuationModelProvider,
  );
  const selectedContinuationModelProvider =
    continuationModelProviders.find((provider) => provider.id === continuationModelProvider) ??
    modelProviders.find((provider) => provider.id === continuationModelProvider);
  // Compute inline instead of useMemo: the compiler cannot preserve memoization
  // over selectedContinuationModelProvider (it may be mutated later), and the
  // derivation is a cheap filter.
  const continuationModelOptions = modelsForProvider(selectedContinuationModelProvider);

  // Keep continuationModelOverride valid for the selected provider by adjusting
  // state during render (not in an effect), comparing against the previous
  // provider/option set so we only reset when the selection actually changes.
  const overrideKey = `${continuationModelProvider}:${continuationModelOptions.join(",")}`;
  const [lastOverrideKey, setLastOverrideKey] = useState("");
  if (lastOverrideKey !== overrideKey) {
    setLastOverrideKey(overrideKey);
    if (!continuationModelProvider) {
      if (continuationModelOverride) setContinuationModelOverride("");
    } else if (!continuationModelOverride || !continuationModelOptions.includes(continuationModelOverride)) {
      setContinuationModelOverride(continuationModelOptions[0] ?? "");
    }
  }

  function continuationModelPayload() {
    const providerID = continuationModelProvider.trim();
    const model = continuationModelOverride.trim();
    const effort = displayReasoningEffort(continuationReasoningEffort);
    // Every Runtime Turn sends the complete resolved selection. `model` is the
    // canonical field; model_override is kept as a compatibility alias.
    const payload: {
      model_provider_id?: string;
      model?: string;
      model_override?: string;
      reasoning_effort: string;
    } = {
      reasoning_effort: effort,
    };
    if (providerID) payload.model_provider_id = providerID;
    if (model) {
      payload.model = model;
      payload.model_override = model;
    }
    return payload;
  }

  function selectContinuationModelProvider(providerID: string) {
    setContinuationModelProvider(providerID);
    const provider = continuationModelProviders.find((item) => item.id === providerID) ??
      modelProviders.find((item) => item.id === providerID);
    setContinuationModelOverride(modelsForProvider(provider)[0] ?? "");
  }

  return {
    continuationModelProvider,
    continuationModelOverride,
    setContinuationModelOverride,
    continuationReasoningEffort,
    setContinuationReasoningEffort,
    setTurnSelectionSeeded,
    currentRuntimeProvider,
    continuationModelProviders,
    continuationModelOptions,
    continuationModelPayload,
    selectContinuationModelProvider,
  };
}
