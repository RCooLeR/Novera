package settings

import (
	"path/filepath"
	"testing"
)

func TestAgentSettingsNormalizeDefaultsAndClamp(t *testing.T) {
	got := (Agent{}).Normalized()
	if got.MaxToolOutputChars != DefaultAgentMaxToolOutputChars {
		t.Errorf("MaxToolOutputChars = %d, want %d", got.MaxToolOutputChars, DefaultAgentMaxToolOutputChars)
	}
	if got.StepBatch != DefaultAgentStepBatch {
		t.Errorf("StepBatch = %d, want %d", got.StepBatch, DefaultAgentStepBatch)
	}
	if got.MaxTotalSteps != DefaultAgentMaxTotalSteps {
		t.Errorf("MaxTotalSteps = %d, want %d", got.MaxTotalSteps, DefaultAgentMaxTotalSteps)
	}
	if got.HistoryWindowGroups != DefaultAgentHistoryWindow {
		t.Errorf("HistoryWindowGroups = %d, want %d", got.HistoryWindowGroups, DefaultAgentHistoryWindow)
	}
	if got.CommandTimeoutSec != DefaultAgentCommandTimeoutSec {
		t.Errorf("CommandTimeoutSec = %d, want %d", got.CommandTimeoutSec, DefaultAgentCommandTimeoutSec)
	}

	got = (Agent{
		MaxToolOutputChars:  10,
		StepBatch:           9999,
		MaxTotalSteps:       1,
		HistoryWindowGroups: 999,
		CommandTimeoutSec:   1,
	}).Normalized()
	if got.MaxToolOutputChars != 1000 {
		t.Errorf("MaxToolOutputChars clamp = %d, want 1000", got.MaxToolOutputChars)
	}
	if got.StepBatch != 500 {
		t.Errorf("StepBatch clamp = %d, want 500", got.StepBatch)
	}
	if got.MaxTotalSteps != 500 {
		t.Errorf("MaxTotalSteps should be at least StepBatch, got %d", got.MaxTotalSteps)
	}
	if got.HistoryWindowGroups != 50 {
		t.Errorf("HistoryWindowGroups clamp = %d, want 50", got.HistoryWindowGroups)
	}
	if got.CommandTimeoutSec != 5 {
		t.Errorf("CommandTimeoutSec clamp = %d, want 5", got.CommandTimeoutSec)
	}
}

func TestSaveLoadAgentSettings(t *testing.T) {
	svc := &Service{path: filepath.Join(t.TempDir(), "settings.json")}
	in := defaults()
	in.Agent = Agent{
		MaxToolOutputChars:  12000,
		StepBatch:           20,
		MaxTotalSteps:       300,
		HistoryWindowGroups: 12,
		CommandTimeoutSec:   180,
	}
	if err := svc.Save(in); err != nil {
		t.Fatal(err)
	}
	got := svc.Load().Agent
	if got != in.Agent {
		t.Fatalf("loaded agent settings = %+v, want %+v", got, in.Agent)
	}
}
