package agent

import (
	"reflect"
	"testing"
)

func TestStartArgs_AppendsDeveloperInstructionsForCodexAppServer(t *testing.T) {
	a := &ACPAgent{
		args:         []string{"app-server", "--listen", "stdio://"},
		systemPrompt: "keep runtime details private",
		protocol:     protocolCodexAppServer,
	}

	got := a.startArgs()
	want := []string{
		"app-server",
		"--listen",
		"stdio://",
		"-c",
		`developer_instructions="keep runtime details private"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("startArgs() = %#v, want %#v", got, want)
	}
}

func TestStartArgs_LegacyACPLeavesArgsUntouched(t *testing.T) {
	a := &ACPAgent{
		args:         []string{"acp"},
		systemPrompt: "keep runtime details private",
		protocol:     protocolLegacyACP,
	}

	got := a.startArgs()
	want := []string{"acp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("startArgs() = %#v, want %#v", got, want)
	}
}

func TestLegacyPromptEntries_PrependsSystemPrompt(t *testing.T) {
	a := &ACPAgent{systemPrompt: "system guardrail"}

	got := a.legacyPromptEntries("hello")
	want := []promptEntry{
		{Type: "text", Text: "system guardrail"},
		{Type: "text", Text: "hello"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacyPromptEntries() = %#v, want %#v", got, want)
	}
}
