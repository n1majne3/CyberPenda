# Model Providers

Manage a Model Provider and its manual Model Catalog without contacting a model service. This recipe proves saved configuration, not a Launch-Ready Model Provider or a successful model call.

## Sub-features

- `provider-entry`: sidebar and direct navigation open the Model Providers Page.
- `provider-create`: save a name, endpoint, fake fixture credential, and Manual Model Entry.
- `provider-limits`: save Model Catalog Limit Overrides and read them after reload.
- `provider-search`: filter saved providers and clear an empty search result.

## How to get to it (user POV)

- Choose `Model providers` under Global settings in the sidebar.
- Open `/model-providers` directly.
- Choose the header `New provider` button. With an empty library, the list also has a `New provider` button.
- Choose a saved provider in the left-hand list to edit it.

## Driving it with agent-browser

Preconditions: Doctor passes; the data belongs to this run; no provider named `Verification Provider` exists. The string `verification-only-not-a-real-api-key` is a fake fixture value, not a credential to any external service.

- **Navigation.** Run `ab click '[aria-label="Global settings"] a[href="/model-providers"]'`, wait for `#model-providers-search`, and save a snapshot. Repeat with `ab open "$UI/model-providers"` for direct navigation.
- **Create entry points.** Run `ab click 'button[aria-label="New provider"]'`, require `#provider-name`, then `ab click 'button[aria-label="Cancel new provider"]'`. With an empty list, run `ab click '[data-testid="model-providers-settings-list"] button'` only after the snapshot confirms that the empty-list `New provider` button is the sole button in that list column. Require the same form. If the list is no longer empty, report that entry point blocked instead of deleting an existing provider.
- **Configure the fixture.** Run `ab fill '#provider-name' 'Verification Provider'`, `ab fill '#provider-base-url' 'https://verify.invalid/v1'`, `ab fill '#provider-api-key' 'verification-only-not-a-real-api-key'`, and `ab check '#provider-protocol-openai_chat_completions'`. Run `ab fill '#provider-manual-models' 'verification-model'`, then `ab select '#provider-default-model' 'verification-model'`. Leave the other protocol choices at their visible defaults and capture them with `ab snapshot`.
- **Save.** Run `ab screenshot "$EVIDENCE/provider-form.png"` before `ab find role button click --name 'Create provider' --exact`. Require the provider in the left-hand list and the generated API key environment-variable name in the detail pane. Save a result snapshot. Do not click `Refresh models` or `Refresh capability cache`.
- **Limits.** Run `ab find label 'verification-model context window' fill '65536'` and `ab find label 'verification-model max output tokens' fill '4096'`. Run `ab find role button click --name 'Save provider' --exact`. Reload, then select the provider's list button from the current snapshot. Require `#provider-name` to contain `Verification Provider`, `#provider-default-model` to contain `verification-model`, and both numeric inputs to retain their values.
- **Search.** Run `ab fill '#model-providers-search' 'Verification Provider'`; require the provider row. Run `ab fill '#model-providers-search' 'no-such-verification-provider'`; require `No matching providers`. Run `ab find role button click --name 'Clear search' --exact`; require the saved provider again.
- **Persistence proof.** Run `ab eval 'fetch("/api/model-providers").then(r => { if (!r.ok) throw new Error(String(r.status)); return r.json(); }).then(x => x.providers.filter(p => p.name === "Verification Provider"))' > "$EVIDENCE/provider-persisted.json"`. Require one provider with the endpoint, manual/default model, and numeric limit overrides. Save `ab snapshot > "$EVIDENCE/provider-persisted.txt"` and `ab screenshot "$EVIDENCE/provider-persisted.png"`.

## Gotchas

- Creation needs a nonempty API key field. Use only the fake fixture value in this isolated instance. Do not copy keys or `.env` files from the operator's normal setup.
- The saved API key is not shown again. Its input can be empty while the UI says the key is configured. Do not treat an empty password input as a failed save.
- `Refresh` in the page header reloads local records. `Refresh models` contacts the Model Provider, and `Refresh capability cache` performs an external refresh. Those are different journeys and are excluded here.
- A fake endpoint and key can satisfy configuration checks but cannot prove model connectivity. Do not launch a Runtime with this provider.
- Catalog hints can use a local daemon request when a model name changes. That is not a Model Catalog Refresh. Capture network requests to distinguish local lookup from an external refresh request.
- The provider list button's accessible name includes endpoint and status text. Use its fresh snapshot ref, not an exact-name assumption.
- Deleting a Model Provider does not remove its API key binding. Let isolated-instance cleanup remove this fixture rather than making a partial record cleanup claim.
