package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNotifyConfigJSON(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"notify":{"noBell":true,"command":["notify-send","Sidekick"]}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Notify.NoBell {
		t.Fatal("NoBell = false, want true")
	}
	if !reflect.DeepEqual(cfg.Notify.Command, []string{"notify-send", "Sidekick"}) {
		t.Fatalf("notify command = %#v", cfg.Notify.Command)
	}

	cfg = (Config{}).WithDefaults()
	if cfg.Notify.NoBell {
		t.Fatal("omitted notify disabled bell; want bell enabled by default")
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := (Config{}).WithDefaults()
	if cfg.Agents.Planner.Name == "" {
		t.Fatal("planner default missing")
	}
	if cfg.Agents.Implementer.Name == "" {
		t.Fatal("implementer default missing")
	}
	if len(cfg.Agents.Reviewers) != 2 {
		t.Fatalf("reviewer defaults = %d, want 2", len(cfg.Agents.Reviewers))
	}
	if cfg.Agents.Learner.Name == "" {
		t.Fatal("learner default missing")
	}
	if !reflect.DeepEqual(cfg.Gate.Command, []string{"no-mistakes", "-y"}) {
		t.Fatalf("gate command = %#v", cfg.Gate.Command)
	}
	if cfg.MaxReviewCycles != 3 {
		t.Fatalf("max review cycles = %d, want 3", cfg.MaxReviewCycles)
	}
}
