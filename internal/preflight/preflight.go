// Package preflight runs the recorded startup checks that determine whether a
// task can launch its runtime. Preflight fails before runtime execution when a
// required runtime profile, configuration, sandbox, or credential resolution is
// missing. A preflight failure prevents runtime launch and is recorded in the
// audit log (by the caller, not here).
package preflight

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"pentest/internal/credential"
	"pentest/internal/runtimeprofile"
)

// CheckStatus is the outcome for a single preflight check.
type CheckStatus string

const (
	CheckPass CheckStatus = "pass"
	CheckFail CheckStatus = "fail"
)

// Check is one named preflight result.
type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
}

// Result is the full preflight outcome for a task launch.
type Result struct {
	Pass   bool    `json:"pass"`
	Checks []Check `json:"checks"`
}

// Request describes what to validate for a task launch.
type Request struct {
	// RuntimeProfileID is the id of the runtime profile the task will use.
	RuntimeProfileID string
	// ProjectID scopes credential resolution. Project defaults may be empty when
	// the task overrides them; the caller decides whether that is allowed.
	ProjectID string
	// CredentialRefsToResolve forces resolution of these references in addition
	// to whatever the runtime profile declares. Useful when project defaults add
	// references the profile does not.
	CredentialRefsToResolve []string
	// Runner is the selected runner. An empty runner defaults to sandbox.
	Runner string
	// HostActivated is true when the operator explicitly confirmed host runner.
	HostActivated bool
	// YOLO skips per-action approvals (smoke / trusted operator path).
	YOLO bool
}

// ProfileGetter loads runtime profiles for preflight checks.
type ProfileGetter interface {
	Get(id string) (runtimeprofile.Profile, error)
}

// Service runs preflight against the runtime profile and credential services.
type Service struct {
	profiles ProfileGetter
	creds    *credential.Service
}

// NewService returns a preflight Service.
func NewService(profiles ProfileGetter, creds *credential.Service) *Service {
	return &Service{profiles: profiles, creds: creds}
}

// Run executes all preflight checks for a launch request.
func (s *Service) Run(ctx context.Context, request Request) Result {
	result := Result{Pass: true}

	// Check 1: the runtime profile exists and is loadable.
	profile, err := s.profiles.Get(request.RuntimeProfileID)
	if err != nil {
		result.add(Check{
			Name:   "runtime_profile",
			Status: CheckFail,
			Detail: notFoundOrError("runtime profile", request.RuntimeProfileID, err),
		})
		// Without a profile we cannot resolve credential refs, but we still run
		// the runner check so the result lists every problem.
	} else {
		result.add(Check{Name: "runtime_profile", Status: CheckPass})
	}

	// Check 2: the selected runner is valid. Empty defaults to sandbox.
	runner := request.Runner
	if runner == "" {
		runner = "sandbox"
	}
	if runner != "sandbox" && runner != "host" {
		result.add(Check{
			Name:   "runner",
			Status: CheckFail,
			Detail: fmt.Sprintf("unsupported runner %q (expected sandbox or host)", runner),
		})
	} else {
		result.add(Check{Name: "runner", Status: CheckPass})
	}

	if runner == "host" && !request.HostActivated && !request.YOLO {
		result.add(Check{
			Name:   "host_activation",
			Status: CheckFail,
			Detail: "host runner requires explicit activation or YOLO mode",
		})
	} else if runner == "host" {
		result.add(Check{Name: "host_activation", Status: CheckPass})
	}

	// Check 3: inline profile API keys or every credential reference resolves.
	if runtimeprofile.HasInlineAPIKeys(profile) {
		result.add(Check{Name: "credentials", Status: CheckPass, Detail: "inline profile API keys configured"})
		return result
	}
	refs := collectRefs(profile, request)
	if len(refs) == 0 {
		result.add(Check{Name: "credentials", Status: CheckPass, Detail: "no credential references"})
	} else {
		anyMissing := false
		for _, ref := range refs {
			if ctx.Err() != nil {
				result.add(Check{
					Name:   "credentials",
					Status: CheckFail,
					Detail: "preflight cancelled",
				})
				return result
			}
			resolution, err := s.creds.Resolve(ref, request.ProjectID)
			if err != nil {
				result.add(Check{
					Name:   "credentials",
					Status: CheckFail,
					Detail: fmt.Sprintf("credential %q: %v", ref, err),
				})
				anyMissing = true
				continue
			}
			if resolution.Disabled {
				result.add(Check{
					Name:   "credentials",
					Status: CheckFail,
					Detail: fmt.Sprintf("credential %q is disabled for this project", ref),
				})
				anyMissing = true
				continue
			}
			if !resolution.Found {
				result.add(Check{
					Name:   "credentials",
					Status: CheckFail,
					Detail: fmt.Sprintf("credential %q has no binding (project or global)", ref),
				})
				anyMissing = true
			}
		}
		if !anyMissing {
			result.add(Check{Name: "credentials", Status: CheckPass})
		}
	}

	return result
}

func (r *Result) add(check Check) {
	r.Checks = append(r.Checks, check)
	if check.Status == CheckFail {
		r.Pass = false
	}
}

func collectRefs(profile runtimeprofile.Profile, request Request) []string {
	seen := map[string]bool{}
	var refs []string
	add := func(ref string) {
		ref = trim(ref)
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	for _, ref := range profile.Fields.CredentialRefs {
		add(ref)
	}
	for _, ref := range request.CredentialRefsToResolve {
		add(ref)
	}
	return refs
}

func notFoundOrError(kind, id string, err error) string {
	if errors.Is(err, runtimeprofile.ErrNotFound) {
		return fmt.Sprintf("%s %q not found", kind, id)
	}
	return fmt.Sprintf("load %s: %v", kind, err)
}

func trim(s string) string {
	return strings.TrimSpace(s)
}
