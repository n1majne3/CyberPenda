package daemon

const disabledBlackboardInitialStateFileReminder = "You may use a state file to keep a lightweight work trace."

func initialDisabledBlackboardLaunchGoal(goal string) string {
	return goal + "\n\n" + disabledBlackboardInitialStateFileReminder
}
