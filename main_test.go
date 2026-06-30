package main

import (
	"reflect"
	"testing"
)

func TestSlug(t *testing.T) {
	got := slug("Implement Sidekick: plan -> code + review!")
	want := "implement-sidekick-plan-code-review"
	if got != want {
		t.Fatalf("slug() = %q, want %q", got, want)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain/path-1": "plain/path-1",
		"":             "''",
		"has space":    "'has space'",
		"it's fine":    "'it'\\''s fine'",
	}
	for input, want := range cases {
		if got := shellQuote(input); got != want {
			t.Fatalf("shellQuote(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCommandForAgentPromptModes(t *testing.T) {
	agent := AgentConfig{Name: "planner", Command: []string{"agent", "--model", "plan"}}

	stdinCmd, err := commandForAgent(agent, "prompt text", "prompt.md")
	if err != nil {
		t.Fatalf("stdin mode: %v", err)
	}
	if stdinCmd.Path != "agent" || !reflect.DeepEqual(stdinCmd.Args, []string{"agent", "--model", "plan"}) {
		t.Fatalf("stdin args = %#v", stdinCmd.Args)
	}

	agent.PromptMode = "arg"
	argCmd, err := commandForAgent(agent, "prompt text", "prompt.md")
	if err != nil {
		t.Fatalf("arg mode: %v", err)
	}
	if !reflect.DeepEqual(argCmd.Args, []string{"agent", "--model", "plan", "prompt text"}) {
		t.Fatalf("arg args = %#v", argCmd.Args)
	}

	agent.PromptMode = "file"
	fileCmd, err := commandForAgent(agent, "prompt text", "prompt.md")
	if err != nil {
		t.Fatalf("file mode: %v", err)
	}
	if !reflect.DeepEqual(fileCmd.Args, []string{"agent", "--model", "plan", "prompt.md"}) {
		t.Fatalf("file args = %#v", fileCmd.Args)
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := (Config{}).withDefaults()
	if cfg.Agents.Planner.Name == "" {
		t.Fatal("planner default missing")
	}
	if cfg.Agents.Implementer.Name == "" {
		t.Fatal("implementer default missing")
	}
	if len(cfg.Agents.Reviewers) != 2 {
		t.Fatalf("reviewer defaults = %d, want 2", len(cfg.Agents.Reviewers))
	}
	if !reflect.DeepEqual(cfg.Gate.Command, []string{"no-mistakes", "-y"}) {
		t.Fatalf("gate command = %#v", cfg.Gate.Command)
	}
}
