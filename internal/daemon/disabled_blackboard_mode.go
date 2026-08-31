package daemon

import "pentest/internal/runner"

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
		launch.goal += "\n\n" + disabledBlackboardRuntimeLaunchStateFileReminder
		launch.projection = runner.BlackboardProjectionOmitted
	}
	return launch
}
