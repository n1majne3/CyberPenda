package daemon

import "pentest/internal/runner"

const disabledBlackboardInitialStateFileReminder = "You may use a state file to keep a lightweight work trace."

type initialOwnerBlackboardLaunch struct {
	goal       string
	projection runner.BlackboardProjection
}

// resolveInitialOwnerBlackboardLaunch maps each owner's typed Disabled mode
// decision to the shared initial Runtime launch boundary.
func resolveInitialOwnerBlackboardLaunch(goal string, disabled bool) initialOwnerBlackboardLaunch {
	launch := initialOwnerBlackboardLaunch{goal: goal, projection: runner.BlackboardProjectionRequired}
	if disabled {
		launch.goal += "\n\n" + disabledBlackboardInitialStateFileReminder
		launch.projection = runner.BlackboardProjectionOmitted
	}
	return launch
}
