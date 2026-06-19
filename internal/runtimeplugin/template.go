package runtimeplugin

import (
	"fmt"
	"strings"
)

type RenderContext struct {
	Scalars map[string]string
	Lists   map[string][]string
}

func RenderLaunch(template LaunchTemplate, ctx RenderContext) ([]string, error) {
	args := suppressSingletonDefaults(template.Args, template.SingletonOptions, ctx.Lists["custom_args"])
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if i+1 < len(args) {
			if name, ok := placeholderName(args[i+1]); ok && optionalPrefix(arg) && !listPlaceholder(name, ctx) && placeholderEmpty(name, ctx) {
				i++
				continue
			}
		}
		if name, ok := placeholderName(arg); ok {
			if values, ok := ctx.Lists[name]; ok {
				out = append(out, nonEmpty(values)...)
				continue
			}
			value := strings.TrimSpace(ctx.Scalars[name])
			if value != "" {
				out = append(out, value)
			}
			continue
		}
		rendered, err := renderScalarString(arg, ctx.Scalars)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(rendered) != "" {
			out = append(out, rendered)
		}
	}
	return out, nil
}

func RenderEnv(template map[string]string, ctx RenderContext) (map[string]string, error) {
	if len(template) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for key, value := range template {
		rendered, err := renderScalarString(value, ctx.Scalars)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(rendered) != "" {
			out[key] = rendered
		}
	}
	return out, nil
}

func suppressSingletonDefaults(args []string, groups []SingletonOption, customArgs []string) []string {
	if len(groups) == 0 || len(customArgs) == 0 {
		return append([]string(nil), args...)
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		suppressed := false
		for _, group := range groups {
			if !containsOption(group.Options, arg) || !customHasAny(customArgs, group.Options) {
				continue
			}
			suppressed = true
			i += group.Arity
			break
		}
		if !suppressed {
			out = append(out, arg)
		}
	}
	return out
}

func customHasAny(args []string, options []string) bool {
	for _, option := range options {
		if HasCLIOption(args, option) {
			return true
		}
	}
	return false
}

func containsOption(options []string, arg string) bool {
	for _, option := range options {
		if arg == option {
			return true
		}
	}
	return false
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func optionalPrefix(arg string) bool {
	return strings.HasPrefix(arg, "-")
}

func listPlaceholder(name string, ctx RenderContext) bool {
	_, ok := ctx.Lists[name]
	return ok
}

func placeholderEmpty(name string, ctx RenderContext) bool {
	if values, ok := ctx.Lists[name]; ok {
		return len(nonEmpty(values)) == 0
	}
	return strings.TrimSpace(ctx.Scalars[name]) == ""
}

func renderScalarString(input string, values map[string]string) (string, error) {
	output := input
	for {
		start := strings.Index(output, "{{")
		if start < 0 {
			return output, nil
		}
		end := strings.Index(output[start+2:], "}}")
		if end < 0 {
			return "", fmt.Errorf("%w: unterminated template in %q", ErrInvalidPlugin, input)
		}
		end += start + 2
		name := strings.TrimSpace(output[start+2 : end])
		output = output[:start] + values[name] + output[end+2:]
	}
}

func placeholderName(input string) (string, bool) {
	if !strings.HasPrefix(input, "{{") || !strings.HasSuffix(input, "}}") {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(input, "{{"), "}}"))
	if name == "" {
		return "", false
	}
	return name, true
}

func HasCLIOption(args []string, option string) bool {
	for _, arg := range args {
		if arg == option || strings.HasPrefix(arg, option+"=") {
			return true
		}
	}
	return false
}
