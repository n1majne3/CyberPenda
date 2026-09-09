package hostedcontroller

import (
	"fmt"
	"net/url"
	"strings"

	"pentest/internal/modelprovider"
	"pentest/internal/runtimeplugin"
)

// PIAdditionalModelSlotCount is the fixed number of optional additional-model
// slots the Hosted Pi profile accepts.
const PIAdditionalModelSlotCount = 3

// piAdditionalModelEnvPrefix is the shared prefix of every additional-model
// variable: CYBERPENDA_PI_ADDITIONAL_MODEL_<slot>[_PROTOCOL|_BASE_URL|_API_KEY].
const piAdditionalModelEnvPrefix = "CYBERPENDA_PI_ADDITIONAL_MODEL_"

// PIAdditionalModel is one validated additional-model slot with its effective
// provider tuple. Unset overrides already inherited the parent values. The
// API key stays in memory: it never reaches persisted Runtime Configuration
// Snapshots or Runtime Profile fields.
type PIAdditionalModel struct {
	Slot     int
	Model    string
	Protocol string
	BaseURL  string
	APIKey   string
}

// HostedModelGroup is one Model Provider group in the Hosted bootstrap plan.
// The parent group is first and keeps its fixed name. A group holds the
// complete effective provider tuple; BaseURL keeps the operator input while
// grouping compares normalized endpoints. Models keeps slot order; the first
// model is the catalog default.
type HostedModelGroup struct {
	Name     string
	Protocol string
	BaseURL  string
	APIKey   string
	Models   []string
	Parent   bool
}

// parsePIAdditionalModels reads the numbered additional-model variables and
// resolves every unset override from the parent configuration. Absent and
// empty values are different states: an absent variable is an unused field,
// a present but blank value is invalid.
func parsePIAdditionalModels(env map[string]string, parent Config) ([]PIAdditionalModel, error) {
	if !anyPIAdditionalModelVarPresent(env) {
		return nil, nil
	}
	if parent.Runtime != RuntimePi {
		return nil, fmt.Errorf("%w: %s* requires CYBERPENDA_RUNTIME=%s", ErrInvalidConfig, piAdditionalModelEnvPrefix, RuntimePi)
	}
	var out []PIAdditionalModel
	for slot := 1; slot <= PIAdditionalModelSlotCount; slot++ {
		varName := fmt.Sprintf("%s%d", piAdditionalModelEnvPrefix, slot)
		model, modelPresent := env[varName]
		protocol, protocolPresent := env[varName+"_PROTOCOL"]
		baseURL, baseURLPresent := env[varName+"_BASE_URL"]
		apiKey, apiKeyPresent := env[varName+"_API_KEY"]
		if !modelPresent {
			for field, present := range map[string]bool{"PROTOCOL": protocolPresent, "BASE_URL": baseURLPresent, "API_KEY": apiKeyPresent} {
				if present {
					return nil, fmt.Errorf("%w: %s_%s is set without %s", ErrInvalidConfig, varName, field, varName)
				}
			}
			continue
		}
		model = strings.TrimSpace(model)
		if model == "" {
			return nil, fmt.Errorf("%w: %s is blank", ErrInvalidConfig, varName)
		}
		effective := PIAdditionalModel{Slot: slot, Model: model, Protocol: parent.ModelProtocol, BaseURL: parent.ModelBaseURL, APIKey: parent.ModelAPIKey}
		if protocolPresent {
			protocol = strings.TrimSpace(protocol)
			if protocol == "" {
				return nil, fmt.Errorf("%w: %s_PROTOCOL is blank", ErrInvalidConfig, varName)
			}
			effective.Protocol = protocol
		}
		if baseURLPresent {
			baseURL = strings.TrimSpace(baseURL)
			if baseURL == "" {
				return nil, fmt.Errorf("%w: %s_BASE_URL is blank", ErrInvalidConfig, varName)
			}
			effective.BaseURL = baseURL
		}
		if apiKeyPresent {
			apiKey = strings.TrimSpace(apiKey)
			if apiKey == "" {
				return nil, fmt.Errorf("%w: %s_API_KEY is blank", ErrInvalidConfig, varName)
			}
			effective.APIKey = apiKey
		}
		if !runtimeplugin.BuiltinSupportsModelProtocol(RuntimePi, effective.Protocol) {
			return nil, fmt.Errorf("%w: %s_PROTOCOL %q is not supported by the pi Runtime", ErrInvalidConfig, varName, effective.Protocol)
		}
		if err := validateHostedModelBaseURL(effective.BaseURL); err != nil {
			return nil, fmt.Errorf("%w: %s_BASE_URL %v", ErrInvalidConfig, varName, err)
		}
		out = append(out, effective)
	}
	return out, nil
}

func anyPIAdditionalModelVarPresent(env map[string]string) bool {
	for slot := 1; slot <= PIAdditionalModelSlotCount; slot++ {
		for _, suffix := range []string{"", "_PROTOCOL", "_BASE_URL", "_API_KEY"} {
			if _, present := env[piAdditionalModelEnvPrefix+fmt.Sprint(slot)+suffix]; present {
				return true
			}
		}
	}
	return false
}

// validateHostedModelBaseURL applies the Hosted gateway restriction shared by
// the parent model and every additional slot: plain HTTP, a .tsecbench.gw
// host, no user information, query, fragment, or operation suffix.
func validateHostedModelBaseURL(raw string) error {
	invalid := fmt.Errorf("%q is not an operation-free HTTP .tsecbench.gw base URL", raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".tsecbench.gw") {
		return invalid
	}
	path := strings.TrimRight(strings.ToLower(parsed.Path), "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages"} {
		if strings.HasSuffix(path, suffix) {
			return invalid
		}
	}
	return nil
}

// hostedModelTuple is the normalized grouping identity of one provider
// tuple. Endpoint comparison follows modelprovider.NormalizeEndpointBaseURL
// semantics so cosmetic trailing slashes do not split providers. The API key
// participates in equality only; it never reaches diagnostics.
type hostedModelTuple struct {
	protocol      modelprovider.Protocol
	normalizedURL string
	apiKey        string
}

func hostedModelTupleOf(protocol, baseURL, apiKey string) (hostedModelTuple, error) {
	normalized, err := modelprovider.NormalizeEndpointBaseURL(modelprovider.Protocol(strings.TrimSpace(protocol)), baseURL)
	if err != nil {
		return hostedModelTuple{}, err
	}
	return hostedModelTuple{
		protocol:      modelprovider.Protocol(strings.TrimSpace(protocol)),
		normalizedURL: normalized,
		apiKey:        strings.TrimSpace(apiKey),
	}, nil
}

// PlanHostedModelGroups turns one Hosted Runtime bootstrap into the
// parent-first Model Provider groups bootstrap must create. It is pure: no
// I/O, and API keys participate only in equality checks.
//
// Grouping rules: a model id maps to exactly one provider tuple. Repeating a
// model id with an identical tuple projects it once; repeating it with a
// different tuple fails, including repeats of the parent model. Different
// model ids share one group only when their whole effective tuple matches.
func PlanHostedModelGroups(runtime HostedRuntimeBootstrap) ([]HostedModelGroup, error) {
	parentTuple, err := hostedModelTupleOf(runtime.ModelProtocol, runtime.ModelBaseURL, runtime.ModelAPIKey)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid parent Model Provider tuple: %v", ErrInvalidConfig, err)
	}
	groups := []HostedModelGroup{{
		Name: "TSecBench Hosted Model", Protocol: runtime.ModelProtocol,
		BaseURL: runtime.ModelBaseURL, APIKey: runtime.ModelAPIKey,
		Models: []string{runtime.Model}, Parent: true,
	}}
	tuples := []hostedModelTuple{parentTuple}
	modelGroup := map[string]int{runtime.Model: 0}
	for _, additional := range runtime.PIAdditionalModels {
		tuple, err := hostedModelTupleOf(additional.Protocol, additional.BaseURL, additional.APIKey)
		if err != nil {
			return nil, fmt.Errorf("%w: %s%d has an invalid provider tuple: %v", ErrInvalidConfig, piAdditionalModelEnvPrefix, additional.Slot, err)
		}
		if existing, ok := modelGroup[additional.Model]; ok {
			if tuples[existing] == tuple {
				continue
			}
			return nil, fmt.Errorf("%w: %s%d model %q conflicts with an earlier definition of the same model id; the provider protocol, base URL, or API key differs",
				ErrInvalidConfig, piAdditionalModelEnvPrefix, additional.Slot, additional.Model)
		}
		shared := -1
		for index := range tuples {
			if tuples[index] == tuple {
				shared = index
				break
			}
		}
		if shared >= 0 {
			groups[shared].Models = append(groups[shared].Models, additional.Model)
			modelGroup[additional.Model] = shared
			continue
		}
		groups = append(groups, HostedModelGroup{
			Name:     fmt.Sprintf("TSecBench Hosted Additional Model %d", additional.Slot),
			Protocol: additional.Protocol, BaseURL: additional.BaseURL, APIKey: additional.APIKey,
			Models: []string{additional.Model},
		})
		tuples = append(tuples, tuple)
		modelGroup[additional.Model] = len(groups) - 1
	}
	return groups, nil
}
