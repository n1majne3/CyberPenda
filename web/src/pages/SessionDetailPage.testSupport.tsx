

export const mimoProvider = {
  id: "mimo",
  name: "MiMo",
  base_url: "https://api.example.test/v1",
  protocols: ["openai_responses"],
  api_key_env: "MIMO_API_KEY",
  catalog: { manual: ["mimo-v2"], default_model: "mimo-v2" },
};

export const codexPlugin = {
  schema_version: 1,
  id: "codex",
  name: "Codex",
  binary: { default: "codex" },
  capabilities: { sandbox: true, host: true, mcp_config: true, streaming_transcript: true, resume: true },
  model_provider: { requirement: "required", supported_protocols: ["openai_responses"] },
  profile_schema: { fields: [] },
  config_projection: { primitive: "codex" },
  launch: { args: [] },
  transcript: { parser: "codex" },
};
