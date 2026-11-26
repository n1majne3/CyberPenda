package daemon

import (
	"testing"

	"pentest/internal/runtime"
	"pentest/internal/runtimeplugin"
	"pentest/internal/runtimeprofile"
	"pentest/internal/testsupport/fakeprovider"
)

func TestPrepareNativeSteerForPiInterruptsTheInFlightTurn(t *testing.T) {
	session := fakeprovider.New(fakeprovider.Config{
		SessionID: "session-pi-steer", ActiveTurnID: "turn-pi-work", ActiveTurnKind: runtime.RuntimeTurnKindWork,
		Capabilities: runtimeplugin.Capabilities{
			PersistentSession: true, SendTurn: true, InTurnSteer: true, InterruptThenReplace: true,
		},
	})

	prepared, err := prepareNativeSteerForProvider(session, runtimeprofile.ProviderPi, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Mode != runtime.ProviderSessionModeInterruptThenReplace || prepared.ExpectedProviderTurnID != "turn-pi-work" {
		t.Fatalf("prepared Pi steer = %#v", prepared)
	}
}
