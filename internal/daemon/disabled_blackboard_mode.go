package daemon

import (
	"os"
	"strings"

	"pentest/internal/runner"
)

const disabledBlackboardRuntimeLaunchStateFileReminder = "You may use a state file to keep a lightweight work trace."

type ownerBlackboardRuntimeLaunch struct {
	goal       string
	projection runner.BlackboardProjection
}

// resolveOwnerBlackboardRuntimeLaunch maps each owner's typed Disabled mode
// decision to every initial or replacement Runtime launch boundary.
func resolveOwnerBlackboardRuntimeLaunch(goal string, disabled bool) ownerBlackboardRuntimeLaunch {
	launch := ownerBlackboardRuntimeLaunch{goal: goal, projection: runner.BlackboardProjectionRequired}
	if disabled {
		// The state-file reminder targets local Disabled owners with no other
		// trace mechanism. A Hosted evaluation owns its FGS working trace (the
		// ctf-orchestrator graph), so the reminder is omitted there.
		if strings.TrimSpace(os.Getenv("CYBERPENDA_HOSTED_DATA_ROOT")) == "" {
			launch.goal += "\n\n" + disabledBlackboardRuntimeLaunchStateFileReminder
		}
		launch.projection = runner.BlackboardProjectionOmitted
	}
	return launch
}
