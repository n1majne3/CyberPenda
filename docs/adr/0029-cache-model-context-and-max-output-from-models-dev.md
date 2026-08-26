# Cache model context window and max output from models.dev

CyberPenda does not guess token limits from a model family name. Operators can set per-model **Model Catalog Limit Overrides**, and **Config Projection** otherwise looks up a bundled **Model Capability Cache** generated from models.dev — the same upstream Pi uses. A manual **Model Capability Cache Refresh** updates that cache; it is not part of Task Launch or Preflight. Cache misses keep each Runtime's native conservative fallback and still launch.

## Considered Options

- Store window and max output only on a Runtime Profile: rejected because Pi Global Model Projection writes every catalog model and a Pi Runtime Turn can switch models without restart.
- Infer DeepSeek/GPT/Claude numbers from the model id text: rejected; hosted max completion is already operator-supplied, never guessed.
- Fetch models.dev during Task Launch: rejected; this product is local-first and Catalog Refresh is already a management action.
