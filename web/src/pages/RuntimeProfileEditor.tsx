import { useState } from "react";
import type { ModelProvider, RuntimeExtension, RuntimePlugin } from "@/lib/api";
import { compatibleProtocolsForRuntime, selectableModelProviders, showLegacyModelFields } from "@/pages/runtimeProfileForm";
import { Button } from "@/components/ui";
import { SaveActionButton } from "@/components/SaveActionButton";
import { HIDDEN_PROVIDER_IDS, type RuntimeExtensionFormRef, type ProfileForm, pluginFor, modelProviderModels, pluginHasField, defaultAPIKeyEnv, formatEnv } from "./runtimeProfileEditorModel";
import { RuntimeProfileAdvancedSection } from "./RuntimeProfileAdvancedSection";
import { RuntimeProfileBasicsSection } from "./RuntimeProfileBasicsSection";

export function ProfileEditor({
  title,
  form,
  onChange,
  onSave,
  onCancel,
  saveLabel = "Save",
  saveDisabled,
  savePending,
  hideActions,
  plugins,
  modelProviders,
  extensions,
}: {
  title?: string;
  form: ProfileForm;
  onChange: (form: ProfileForm) => void;
  onSave?: () => void;
  onCancel?: () => void;
  saveLabel?: string;
  saveDisabled?: boolean;
  savePending?: boolean;
  hideActions?: boolean;
  plugins: RuntimePlugin[];
  modelProviders: ModelProvider[];
  extensions: RuntimeExtension[];
}) {
  const [extensionToAdd, setExtensionToAdd] = useState("");
  const [manualExtensionID, setManualExtensionID] = useState("");
  const plugin = pluginFor(plugins, form.provider);
  const selectableProviders = selectableModelProviders(modelProviders, plugin, form.model_provider_id);
  const selectedModelProvider = modelProviders.find((provider) => provider.id === form.model_provider_id);
  const compatibleProtocols = selectedModelProvider ? compatibleProtocolsForRuntime(plugin, selectedModelProvider) : [];
  const providerModels = selectedModelProvider ? modelProviderModels(selectedModelProvider) : [];
  const providerOptions = (plugin
    ? plugins
    : [
        ...plugins,
        {
          schema_version: 1,
          id: form.provider,
          name: form.provider,
          binary: { default: form.provider },
          capabilities: { sandbox: true, host: true, mcp_config: false, streaming_transcript: false, resume: false },
          profile_schema: { fields: [] },
          config_projection: { primitive: "generic_config" },
          launch: { args: ["{{binary}}", "{{goal}}"] },
          transcript: { parser: "plain_runtime_output" },
        },
      ]
  ).filter((p) => p.id === form.provider || !HIDDEN_PROVIDER_IDS.has(p.id));
  const has = (field: string) => pluginHasField(plugin, field);
  const legacyModelFields = showLegacyModelFields(form);
  const apiKeyPlaceholder = defaultAPIKeyEnv(form.provider, plugins) ?? "API_KEY";
  const compatibleExtensions = extensions.filter((extension) =>
    extension.compatible_runtime_plugins.includes(form.provider)
  );
  const extensionByID = new Map(extensions.map((extension) => [extension.id, extension]));
  const availableExtensions = compatibleExtensions.filter(
    (extension) => !form.runtime_extensions.some((ref) => ref.id === extension.id)
  );
  const selectedExtensionID = availableExtensions.some((extension) => extension.id === extensionToAdd)
    ? extensionToAdd
    : availableExtensions[0]?.id || "";
  const trimmedManualExtensionID = manualExtensionID.trim();
  const manualRegistryExtension = extensionByID.get(trimmedManualExtensionID);
  const manualExtensionIncompatible = Boolean(
    manualRegistryExtension && !manualRegistryExtension.compatible_runtime_plugins.includes(form.provider)
  );
  const manualExtensionDuplicate = form.runtime_extensions.some((ref) => ref.id === trimmedManualExtensionID);
  const canAddManualExtension =
    trimmedManualExtensionID !== "" && !manualExtensionDuplicate && !manualExtensionIncompatible;
  const addRuntimeExtension = () => {
    const extension = availableExtensions.find((item) => item.id === selectedExtensionID);
    if (!extension) return;
    onChange({
      ...form,
      runtime_extensions: [
        ...form.runtime_extensions,
        { id: extension.id, enabled: true, config: formatEnv(extension.config) },
      ],
    });
    setExtensionToAdd("");
  };
  const addManualRuntimeExtension = () => {
    if (!canAddManualExtension) return;
    onChange({
      ...form,
      runtime_extensions: [
        ...form.runtime_extensions,
        {
          id: trimmedManualExtensionID,
          enabled: true,
          config: manualRegistryExtension ? formatEnv(manualRegistryExtension.config) : "",
        },
      ],
    });
    setManualExtensionID("");
  };
  const updateRuntimeExtension = (index: number, patch: Partial<RuntimeExtensionFormRef>) => {
    onChange({
      ...form,
      runtime_extensions: form.runtime_extensions.map((ref, i) =>
        i === index ? { ...ref, ...patch } : ref
      ),
    });
  };
  const removeRuntimeExtension = (index: number) => {
    onChange({
      ...form,
      runtime_extensions: form.runtime_extensions.filter((_, i) => i !== index),
    });
  };

  return (
    <div className="space-y-5">
      {title && <h3 className="font-medium">{title}</h3>}
      <RuntimeProfileBasicsSection
        form={form}
        onChange={onChange}
        plugins={plugins}
        extensions={extensions}
        providerOptions={providerOptions}
        selectableProviders={selectableProviders}
        plugin={plugin}
        selectedModelProvider={selectedModelProvider}
        compatibleProtocols={compatibleProtocols}
        providerModels={providerModels}
        has={has}
        legacyModelFields={legacyModelFields}
      />
      <RuntimeProfileAdvancedSection
        has={has}
        form={form}
        onChange={onChange}
        legacyModelFields={legacyModelFields}
        apiKeyPlaceholder={apiKeyPlaceholder}
        selectedExtensionID={selectedExtensionID}
        setExtensionToAdd={setExtensionToAdd}
        availableExtensions={availableExtensions}
        addRuntimeExtension={addRuntimeExtension}
        manualExtensionID={manualExtensionID}
        setManualExtensionID={setManualExtensionID}
        addManualRuntimeExtension={addManualRuntimeExtension}
        canAddManualExtension={canAddManualExtension}
        extensions={extensions}
        manualExtensionIncompatible={manualExtensionIncompatible}
        extensionByID={extensionByID}
        updateRuntimeExtension={updateRuntimeExtension}
        removeRuntimeExtension={removeRuntimeExtension}
        compatibleExtensions={compatibleExtensions}
      />
      {form.provider === "claude_code" && form.endpoint.includes("bigmodel.cn") && (
        <div className="space-y-1 rounded-lg border border-border bg-muted/30 p-3 text-xs text-muted-foreground">
          <p className="font-medium text-foreground">Zhipu GLM runtime notes</p>
          <p>Endpoint: use <code className="text-[11px]">https://open.bigmodel.cn/api/anthropic</code> (not Minimax).</p>
          <p>Launch adds <code className="text-[11px]">--strict-mcp-config --mcp-config workdir/.mcp.json</code>; smoke may need <code className="text-[11px]">--permission-mode bypassPermissions</code> in custom args.</p>
          <p>Third-party APIs may not expose local MCP tools in the model tool list — allow JSON-RPC fallback to PENTEST_MCP_URL.</p>
        </div>
      )}
      {form.provider === "pi" && form.default_runner === "sandbox" && (
        <p className="text-[11px] text-muted-foreground">
          Pi sandbox sets <code>PI_CODING_AGENT_DIR=/task/runtime-home/pi/agent</code>; pi is preinstalled in <code>ghcr.io/n1majne3/cyberpenda-sandbox:latest</code>.
        </p>
      )}
      {!hideActions && (
        <div className="flex gap-2">
          <SaveActionButton
            label={saveLabel}
            pending={savePending}
            disabled={saveDisabled}
            onClick={onSave}
          />
          {onCancel && (
            <Button size="sm" variant="ghost" onClick={onCancel}>
              Cancel
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
