package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLLMDefaultsPreferLocalModels(t *testing.T) {
	got := defaults().LLM
	if got.Provider != DefaultLLMProvider {
		t.Errorf("Provider = %q, want %q", got.Provider, DefaultLLMProvider)
	}
	if got.BaseURL != DefaultLLMBaseURL {
		t.Errorf("BaseURL = %q, want %q", got.BaseURL, DefaultLLMBaseURL)
	}
	if got.Model != DefaultLLMModel {
		t.Errorf("Model = %q, want %q", got.Model, DefaultLLMModel)
	}
}

func TestLLMNormalizeOnlyDefaultsModelForOllama(t *testing.T) {
	got := (LLM{}).Normalized()
	if got.Model != DefaultLLMModel {
		t.Errorf("empty LLM model = %q, want %q", got.Model, DefaultLLMModel)
	}

	custom := (LLM{Provider: "custom"}).Normalized()
	if custom.Model != "" {
		t.Errorf("custom LLM model = %q, want empty", custom.Model)
	}
	if custom.BaseURL != "" {
		t.Errorf("custom LLM baseURL = %q, want empty", custom.BaseURL)
	}
}

func TestLoadMigratesBlankOllamaModel(t *testing.T) {
	svc := &Service{path: filepath.Join(t.TempDir(), "settings.json")}
	raw := []byte(`{"llm":{"provider":"ollama","baseURL":"http://localhost:11434/v1","model":""}}`)
	if err := os.MkdirAll(filepath.Dir(svc.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svc.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := svc.Load().LLM.Model; got != DefaultLLMModel {
		t.Fatalf("loaded model = %q, want %q", got, DefaultLLMModel)
	}
}

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
