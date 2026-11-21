package runtimeplugin

import (
	"testing"
	"time"
)

func TestRenderLaunchDoesNotExpandPlaceholdersInsideValues(t *testing.T) {
	done := make(chan []string, 1)
	errs := make(chan error, 1)
	go func() {
		args, err := RenderLaunch(LaunchTemplate{Args: []string{"--goal={{goal}}", "--open={{open}}"}}, RenderContext{
			Scalars: map[string]string{"goal": "find {{goal}}", "open": "text {{ only"},
		})
		if err != nil {
			errs <- err
			return
		}
		done <- args
	}()
	select {
	case err := <-errs:
		t.Fatalf("render: %v", err)
	case args := <-done:
		if len(args) != 2 || args[0] != "--goal=find {{goal}}" || args[1] != "--open=text {{ only" {
			t.Fatalf("args = %q", args)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("render did not finish: a placeholder inside a value was expanded again")
	}
}
