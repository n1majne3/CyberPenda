# Asynchronous test contracts

## Wait for the asserted state

An accepted HTTP response starts background work. It does not prove that the work is complete.

- Use `waitForTaskRunning` before assertions about a running Task. It checks Harness ownership, durable Task status, and the latest Continuation status together.
- Use `waitForHarnessReleased` only after work has started and its exit was requested or observed. An empty ownership registry before launch is not an exit signal.
- Wait for a Provider Turn ID or a fixture channel before an action that requires the Provider to have entered that Turn.
- Wait for the exact receipt or durable event before asserting settlement. A Provider request count proves dispatch only.
- For Session tests, check the expected Continuation number and its durable status. Do not use a bound Provider session as a completion signal.

Do not change `IsActive` to mean readiness. The Harness registers ownership before it writes `running` so Stop and daemon shutdown can cancel a launch in progress. It can also retain ownership while terminal state is being settled.

Use a bounded condition wait with the last observed state in its failure message. Polling a durable condition is valid. A fixed sleep followed by an assertion is not synchronization. Use explicit timestamps when ordering time-based records.

For subprocess deadlock tests, separate fixture startup from the operation under test. Do not treat the process-wide deadline as an HTTP latency requirement. A watchdog must report goroutine stacks and terminate the isolated helper if the operation stays blocked.

## Reproduce scheduling gaps

For a race, hold the disputed boundary open with a channel or a durable fixture state. Confirm that the assertion fails before the fix. `TestTaskStartupWaitRequiresDurableContinuation` covers both partial startup states without depending on scheduler timing.

Keep fake Runtimes alive with a release channel when the test needs a live Runtime. Release or cancel all work before closing the Store. Keep server cleanup registered after temporary directories are created, so background work ends before those directories are removed.

## Checks

Run the affected tests, then `make test-concurrency` for lifecycle changes. This runs the race detector in random test order with one and four logical CPUs. Every scheduled execution must pass; this is not retry-on-failure.

For changes to shared waiting, launch, or shutdown code, also run the complete daemon suite with `-shuffle=on -count=2`. Record the seed printed on failure. Use that seed to reproduce the same order.

Do not accept a race fix solely because repeated runs happen to pass. Keep a regression test for the disputed state boundary. Do not increase sleeps, silently retry failed tests, or skip assertions to obtain a green check.
