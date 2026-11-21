package daemon

import (
	"errors"

	"pentest/internal/runtimeextension"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
)

func runtimePluginRegistry(dirs []string) (*runtimeplugin.Registry, error) {
	plugins := runtimeplugin.BuiltinPlugins()
	var errs []error
	for _, dir := range dirs {
		loaded, loadErrs := runtimeplugin.LoadDirectory(dir)
		plugins = append(plugins, loaded...)
		errs = append(errs, loadErrs...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return runtimeplugin.NewRegistry(plugins)
}

func runtimeExtensionRegistry(dirs []string) (*runtimeextension.Registry, error) {
	var extensions []runtimeextension.Extension
	var errs []error
	for _, dir := range dirs {
		loaded, loadErrs := runtimeextension.LoadDirectory(dir)
		extensions = append(extensions, loaded...)
		errs = append(errs, loadErrs...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return runtimeextension.NewRegistry(extensions)
}

func runtimeProfileProviders(registry *runtimeplugin.Registry) []runtimeprofile.Provider {
	ids := registry.IDs()
	providers := make([]runtimeprofile.Provider, 0, len(ids))
	for _, id := range ids {
		providers = append(providers, runtimeprofile.Provider(id))
	}
	return providers
}
