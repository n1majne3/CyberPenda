package runtimeplugin

import (
	"fmt"
	"sort"
)

type Registry struct {
	plugins map[string]Plugin
	order   []string
}

func NewRegistry(plugins []Plugin) (*Registry, error) {
	registry := &Registry{plugins: map[string]Plugin{}}
	for _, plugin := range plugins {
		if err := Validate(plugin); err != nil {
			return nil, err
		}
		if _, exists := registry.plugins[plugin.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate id %q", ErrInvalidPlugin, plugin.ID)
		}
		registry.plugins[plugin.ID] = clonePlugin(plugin)
		registry.order = append(registry.order, plugin.ID)
	}
	sort.Strings(registry.order)
	return registry, nil
}

func BuiltinRegistry() (*Registry, error) {
	return NewRegistry(BuiltinPlugins())
}

func MustBuiltinRegistry() *Registry {
	registry, err := BuiltinRegistry()
	if err != nil {
		panic(err)
	}
	return registry
}

func (r *Registry) Get(id string) (Plugin, bool) {
	if r == nil {
		return Plugin{}, false
	}
	plugin, ok := r.plugins[id]
	if !ok {
		return Plugin{}, false
	}
	return clonePlugin(plugin), true
}

func (r *Registry) Has(id string) bool {
	if r == nil {
		return false
	}
	_, ok := r.plugins[id]
	return ok
}

func (r *Registry) List() []Plugin {
	if r == nil {
		return nil
	}
	out := make([]Plugin, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, clonePlugin(r.plugins[id]))
	}
	return out
}

func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

func clonePlugin(plugin Plugin) Plugin {
	clone := plugin
	clone.ProfileSchema.Fields = append([]ProfileField(nil), plugin.ProfileSchema.Fields...)
	clone.Launch.Args = append([]string(nil), plugin.Launch.Args...)
	clone.Launch.SingletonOptions = append([]SingletonOption(nil), plugin.Launch.SingletonOptions...)
	for i := range clone.Launch.SingletonOptions {
		clone.Launch.SingletonOptions[i].Options = append([]string(nil), plugin.Launch.SingletonOptions[i].Options...)
	}
	if plugin.ProcessEnv != nil {
		clone.ProcessEnv = map[string]string{}
		for key, value := range plugin.ProcessEnv {
			clone.ProcessEnv[key] = value
		}
	}
	clone.CredentialEnv = append([]string(nil), plugin.CredentialEnv...)
	return clone
}
