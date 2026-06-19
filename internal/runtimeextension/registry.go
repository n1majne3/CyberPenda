package runtimeextension

import (
	"fmt"
	"sort"
)

type Registry struct {
	extensions map[string]Extension
	order      []string
}

func NewRegistry(extensions []Extension) (*Registry, error) {
	registry := &Registry{extensions: map[string]Extension{}}
	for _, extension := range extensions {
		if err := Validate(extension); err != nil {
			return nil, err
		}
		if _, exists := registry.extensions[extension.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate id %q", ErrInvalidExtension, extension.ID)
		}
		registry.extensions[extension.ID] = cloneExtension(extension)
		registry.order = append(registry.order, extension.ID)
	}
	sort.Strings(registry.order)
	return registry, nil
}

func (r *Registry) Get(id string) (Extension, bool) {
	if r == nil {
		return Extension{}, false
	}
	extension, ok := r.extensions[id]
	if !ok {
		return Extension{}, false
	}
	return cloneExtension(extension), true
}

func (r *Registry) List() []Extension {
	if r == nil {
		return nil
	}
	out := make([]Extension, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, cloneExtension(r.extensions[id]))
	}
	return out
}

func cloneExtension(extension Extension) Extension {
	clone := extension
	clone.CompatibleRuntimePlugins = append([]string(nil), extension.CompatibleRuntimePlugins...)
	if extension.Config != nil {
		clone.Config = map[string]string{}
		for key, value := range extension.Config {
			clone.Config[key] = value
		}
	}
	return clone
}
