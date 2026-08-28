package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeSecretStore struct {
	values       map[string]string
	setErr       error
	replaceErr   error
	healthErr    error
	getErr       error
	setCalls     int
	deleteCalls  int
	replaceCalls [][2]string
	replaceHook  func()
}

func (f *fakeSecretStore) Health() error { return f.healthErr }

func (f *fakeSecretStore) Get(ref string) (string, bool) {
	v, ok := f.values[ref]
	return v, ok
}

func (f *fakeSecretStore) GetChecked(ref string) (string, bool, error) {
	if f.healthErr != nil {
		return "", false, f.healthErr
	}
	if f.getErr != nil {
		return "", false, f.getErr
	}
	v, ok := f.values[ref]
	return v, ok, nil
}

func (f *fakeSecretStore) GetManyChecked(refs []string) (map[string]string, error) {
	if f.healthErr != nil {
		return nil, f.healthErr
	}
	if f.getErr != nil {
		return nil, f.getErr
	}
	out := make(map[string]string, len(refs))
	for _, ref := range refs {
		if value, found := f.values[ref]; found {
			out[ref] = value
		}
	}
	return out, nil
}

func (f *fakeSecretStore) Set(ref, value string) error {
	f.setCalls++
	if f.healthErr != nil {
		return f.healthErr
	}
	if f.setErr != nil {
		return f.setErr
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[ref] = value
	return nil
}

func (f *fakeSecretStore) Has(ref string) bool {
	_, ok := f.values[ref]
	return ok
}

func (f *fakeSecretStore) Delete(ref string) error {
	f.deleteCalls++
	if f.healthErr != nil {
		return f.healthErr
	}
	delete(f.values, ref)
	return nil
}

func (f *fakeSecretStore) Replace(oldRef, newRef, value string) error {
	f.replaceCalls = append(f.replaceCalls, [2]string{oldRef, newRef})
	if f.healthErr != nil {
		return f.healthErr
	}
	if f.replaceErr != nil {
		return f.replaceErr
	}
	if _, found := f.values[oldRef]; !found {
		return fmt.Errorf("old secret ref %q does not exist", oldRef)
	}
	delete(f.values, oldRef)
	f.values[newRef] = value
	if f.replaceHook != nil {
		f.replaceHook()
	}
	return nil
}

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

func TestSanitizeBaseURLRemovesCredentialsFromMalformedAuthority(t *testing.T) {
	const secret = "top-secret"
	got := sanitizeBaseURL("https://user:" + secret + "@[/v1")
	if got != "https://[/v1" || strings.Contains(got, secret) {
		t.Fatalf("sanitized malformed URL = %q", got)
	}
	if got := sanitizeBaseURL("local-user@example.test"); got != "local-user@example.test" {
		t.Fatalf("non-URL text was rewritten: %q", got)
	}
	if got := sanitizeBaseURL("https://[::1]/path@revision"); got != "https://[::1]/path@revision" {
		t.Fatalf("path at-sign was mistaken for userinfo: %q", got)
	}
}

func TestSaveDoesNotPersistCredentialsFromMalformedBaseURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	svc := &Service{path: path}
	in := defaults()
	in.LLM.Provider = "custom"
	in.LLM.BaseURL = "https://user:plaintext-secret@[/v1"
	if err := svc.Save(in); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("plaintext-secret")) || !bytes.Contains(raw, []byte(`"baseURL": "https://[/v1"`)) {
		t.Fatalf("persisted settings retained malformed URL credentials: %s", raw)
	}
}

func TestSaveRejectsInvalidRequestTimeouts(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, value := range []int{-1, MinRequestTimeoutSec - 1, MaxRequestTimeoutSec + 1, maxInt} {
		t.Run(fmt.Sprintf("timeout_%d", value), func(t *testing.T) {
			svc := &Service{path: filepath.Join(t.TempDir(), "settings.json")}
			in := defaults()
			in.LLM.RequestTimeoutSec = value
			if err := svc.Save(in); err == nil {
				t.Fatalf("Save accepted requestTimeoutSec=%d", value)
			}
			if _, err := os.Stat(svc.path); !os.IsNotExist(err) {
				t.Fatalf("invalid settings were persisted: %v", err)
			}
		})
	}

	for _, value := range []int{0, MinRequestTimeoutSec, 600, MaxRequestTimeoutSec} {
		t.Run(fmt.Sprintf("allowed_%d", value), func(t *testing.T) {
			svc := &Service{path: filepath.Join(t.TempDir(), "settings.json")}
			in := defaults()
			in.LLM.RequestTimeoutSec = value
			if err := svc.Save(in); err != nil {
				t.Fatalf("Save rejected requestTimeoutSec=%d: %v", value, err)
			}
			want := value
			if want == 0 {
				want = DefaultRequestTimeoutSec
			}
			if got := svc.Load().LLM.RequestTimeoutSec; got != want {
				t.Fatalf("requestTimeoutSec round trip = %d, want %d", got, want)
			}
		})
	}
}

func TestUIAndEditorNumbersAreNormalizedAtPersistenceBoundary(t *testing.T) {
	t.Run("load repairs persisted outliers", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		in := defaults()
		in.UIFontSize = -500
		in.Editor.FontSize = 1 << 30
		in.Editor.TabSize = 0
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}

		svc := &Service{path: path}
		got := svc.Load()
		if got.UIFontSize != MinUIFontSize {
			t.Fatalf("UI font size = %d, want %d", got.UIFontSize, MinUIFontSize)
		}
		if got.Editor.FontSize != MaxEditorFontSize {
			t.Fatalf("editor font size = %d, want %d", got.Editor.FontSize, MaxEditorFontSize)
		}
		if got.Editor.TabSize != DefaultEditorTabSize {
			t.Fatalf("editor tab size = %d, want default %d", got.Editor.TabSize, DefaultEditorTabSize)
		}

		persisted, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var repaired Settings
		if err := json.Unmarshal(persisted, &repaired); err != nil {
			t.Fatal(err)
		}
		if repaired.UIFontSize != MinUIFontSize || repaired.Editor.FontSize != MaxEditorFontSize || repaired.Editor.TabSize != DefaultEditorTabSize {
			t.Fatalf("normalized values were not repaired on disk: %+v", repaired)
		}
	})

	t.Run("save clamps bound input", func(t *testing.T) {
		svc := &Service{path: filepath.Join(t.TempDir(), "settings.json")}
		in := defaults()
		in.UIFontSize = 1 << 30
		in.Editor.FontSize = -10
		in.Editor.TabSize = 1 << 30
		if err := svc.Save(in); err != nil {
			t.Fatal(err)
		}
		got := svc.Load()
		if got.UIFontSize != MaxUIFontSize {
			t.Fatalf("UI font size = %d, want %d", got.UIFontSize, MaxUIFontSize)
		}
		if got.Editor.FontSize != MinEditorFontSize {
			t.Fatalf("editor font size = %d, want %d", got.Editor.FontSize, MinEditorFontSize)
		}
		if got.Editor.TabSize != MaxEditorTabSize {
			t.Fatalf("editor tab size = %d, want %d", got.Editor.TabSize, MaxEditorTabSize)
		}
	})
}

func TestRequestTimeoutCannotOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1
	want := time.Duration(DefaultRequestTimeoutSec) * time.Second
	for _, value := range []int{minInt, -1, MaxRequestTimeoutSec + 1, maxInt} {
		if got := (LLM{RequestTimeoutSec: value}).RequestTimeout(); got != want {
			t.Fatalf("RequestTimeout(%d) = %v, want safe default %v", value, got, want)
		}
	}
}

func TestLoadRepairsPersistedInvalidRequestTimeout(t *testing.T) {
	svc := &Service{path: filepath.Join(t.TempDir(), "settings.json")}
	maxInt := int(^uint(0) >> 1)
	raw := []byte(fmt.Sprintf(`{"llm":{"provider":"ollama","baseURL":"http://localhost:11434/v1","requestTimeoutSec":%d}}`, maxInt))
	if err := os.WriteFile(svc.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := svc.Load().LLM.RequestTimeoutSec; got != DefaultRequestTimeoutSec {
		t.Fatalf("loaded timeout = %d, want %d", got, DefaultRequestTimeoutSec)
	}
	persisted, err := os.ReadFile(svc.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), fmt.Sprintf(`"requestTimeoutSec": %d`, DefaultRequestTimeoutSec)) {
		t.Fatalf("repaired timeout was not persisted: %s", persisted)
	}
}

func TestLoadPreservesDefaultsForLegacyNullMembers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":null,"editor":null}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := (&Service{path: path}).Load()
	want := defaults()
	if got.Theme != want.Theme || got.Editor != want.Editor {
		t.Fatalf("Load() = theme %q, editor %#v; want defaults %q, %#v", got.Theme, got.Editor, want.Theme, want.Editor)
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

func TestLoadMigratesLegacyBadOllamaModel(t *testing.T) {
	svc := &Service{path: filepath.Join(t.TempDir(), "settings.json")}
	raw := []byte(`{"llm":{"provider":"ollama","baseURL":"http://localhost:11434/v1","model":"gemma4:12b-it-q_8_0"}}`)
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

func TestSaveRejectsCallerSelectedDBCredentialForLLM(t *testing.T) {
	secrets := &fakeSecretStore{values: map[string]string{"db.cred.profile-a": "database-password"}}
	svc := &Service{path: filepath.Join(t.TempDir(), "settings.json"), secrets: secrets}
	in := defaults()
	in.LLM = LLM{
		Provider:  "custom",
		BaseURL:   "https://attacker.example/v1",
		Model:     "model",
		APIKeyRef: "db.cred.profile-a",
	}
	if err := svc.Save(in); err != nil {
		t.Fatal(err)
	}
	got := svc.Load().LLM
	if got.APIKeyRef != "" {
		t.Fatalf("caller-selected DB ref survived settings save: %q", got.APIKeyRef)
	}
	if value := secrets.values["db.cred.profile-a"]; value != "database-password" {
		t.Fatalf("DB credential was unexpectedly changed during rejection: %q", value)
	}
}

func TestChangingLLMOriginDetachesAndDeletesOldCredential(t *testing.T) {
	secrets := &fakeSecretStore{values: map[string]string{}}
	svc := &Service{path: filepath.Join(t.TempDir(), "settings.json"), secrets: secrets}
	in := defaults()
	in.LLM = LLM{Provider: "custom", BaseURL: "https://trusted.example/v1", Model: "model"}
	if err := svc.Save(in); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetLLMAPIKey("trusted-key"); err != nil {
		t.Fatal(err)
	}
	oldRef := svc.Load().LLM.APIKeyRef
	if oldRef == "" || secrets.values[oldRef] != "trusted-key" {
		t.Fatalf("origin-bound credential was not stored: ref=%q values=%v", oldRef, secrets.values)
	}

	changed := svc.Load()
	changed.LLM.BaseURL = "https://attacker.example/v1"
	changed.LLM.APIKeyRef = oldRef // adversarial attempt to carry the key across origins
	if err := svc.Save(changed); err != nil {
		t.Fatal(err)
	}
	if got := svc.Load().LLM.APIKeyRef; got != "" {
		t.Fatalf("credential followed provider to a different origin: %q", got)
	}
	if _, ok := secrets.values[oldRef]; ok {
		t.Fatalf("detached origin credential %q was left reusable", oldRef)
	}
}

func TestLoadQuarantinesAllowlistedLegacyLLMRefWithoutRebindingIt(t *testing.T) {
	secrets := &fakeSecretStore{values: map[string]string{legacyLLMAPIKeyRef: "legacy-key"}}
	svc := &Service{path: filepath.Join(t.TempDir(), "settings.json"), secrets: secrets}
	raw := []byte(`{"llm":{"provider":"custom","baseURL":"https://provider.example/v1","model":"model","apiKeyRef":"llm.apikey"}}`)
	if err := os.WriteFile(svc.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got := svc.Load().LLM
	expected, err := llmCredentialRef(got)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKeyRef != legacyLLMAPIKeyRef {
		t.Fatalf("legacy ownership ref = %q, want quarantined %q", got.APIKeyRef, legacyLLMAPIKeyRef)
	}
	if _, ok := secrets.values[expected]; ok {
		t.Fatal("legacy credential was automatically rebound to editable provider metadata")
	}
	if secrets.values[legacyLLMAPIKeyRef] != "legacy-key" {
		t.Fatal("quarantined legacy credential evidence was changed")
	}

	// An unknown historical ref must be detached, not migrated into LLM scope.
	secrets.values["db.cred.other"] = "db-key"
	unknown := []byte(`{"llm":{"provider":"custom","baseURL":"https://provider.example/v1","model":"model","apiKeyRef":"db.cred.other"}}`)
	if err := os.WriteFile(svc.path, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if ref := svc.Load().LLM.APIKeyRef; ref != "" {
		t.Fatalf("unknown legacy ref was migrated: %q", ref)
	}
	if secrets.values["db.cred.other"] != "db-key" {
		t.Fatal("foreign credential was modified while detaching it")
	}
}

func TestUnrelatedSavePreservesQuarantinedLegacyLLMOwnership(t *testing.T) {
	secrets := &fakeSecretStore{values: map[string]string{legacyLLMAPIKeyRef: "legacy-key"}}
	svc := &Service{path: filepath.Join(t.TempDir(), "settings.json"), secrets: secrets}
	raw := []byte(`{"uiFontSize":13,"llm":{"provider":"custom","baseURL":"https://provider.example/v1","model":"model","apiKeyRef":"llm.apikey"}}`)
	if err := os.WriteFile(svc.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	changed := svc.Load()
	changed.UIFontSize = 17
	if err := svc.Save(changed); err != nil {
		t.Fatal(err)
	}

	got := svc.Load()
	if got.UIFontSize != 17 {
		t.Fatalf("UI font size = %d, want 17", got.UIFontSize)
	}
	if got.LLM.APIKeyRef != legacyLLMAPIKeyRef {
		t.Fatalf("legacy ownership ref = %q after unrelated save, want %q", got.LLM.APIKeyRef, legacyLLMAPIKeyRef)
	}
	if value := secrets.values[legacyLLMAPIKeyRef]; value != "legacy-key" {
		t.Fatalf("quarantined legacy credential = %q after unrelated save", value)
	}
	if secrets.deleteCalls != 0 {
		t.Fatalf("unrelated save deleted %d secret refs", secrets.deleteCalls)
	}

	persisted, err := os.ReadFile(svc.path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Settings
	if err := json.Unmarshal(persisted, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.LLM.APIKeyRef != legacyLLMAPIKeyRef {
		t.Fatalf("persisted legacy ownership ref = %q, want %q", decoded.LLM.APIKeyRef, legacyLLMAPIKeyRef)
	}
}

func TestExplicitLegacyLLMReentryUsesAtomicReplace(t *testing.T) {
	in := defaults()
	in.LLM = LLM{
		Provider:  "custom",
		BaseURL:   "https://provider.example/v1",
		Model:     "model",
		APIKeyRef: legacyLLMAPIKeyRef,
	}
	raw, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := llmCredentialRef(in.LLM)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &fakeSecretStore{values: map[string]string{
		legacyLLMAPIKeyRef: "quarantined legacy value",
		expected:           "stale destination value",
	}}
	svc := &Service{path: path, secrets: secrets}

	if err := svc.SetLLMAPIKey("explicit replacement"); err != nil {
		t.Fatal(err)
	}
	if len(secrets.replaceCalls) != 1 || secrets.replaceCalls[0] != [2]string{legacyLLMAPIKeyRef, expected} {
		t.Fatalf("Replace calls = %#v", secrets.replaceCalls)
	}
	if secrets.setCalls != 0 || secrets.deleteCalls != 0 {
		t.Fatalf("legacy replacement used Set/Delete: set=%d delete=%d", secrets.setCalls, secrets.deleteCalls)
	}
	if _, found := secrets.values[legacyLLMAPIKeyRef]; found {
		t.Fatal("legacy ref survived explicit replacement")
	}
	if got := secrets.values[expected]; got != "explicit replacement" {
		t.Fatalf("scoped value = %q", got)
	}
	if got := svc.Load().LLM.APIKeyRef; got != expected {
		t.Fatalf("persisted API key ref = %q, want %q", got, expected)
	}
}

func TestLoadRecoversInterruptedExplicitLegacyLLMReplacement(t *testing.T) {
	in := defaults()
	in.LLM = LLM{
		Provider:  "custom",
		BaseURL:   "https://provider.example/v1",
		Model:     "model",
		APIKeyRef: legacyLLMAPIKeyRef,
	}
	expected, err := llmCredentialRef(in.LLM)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := &fakeSecretStore{values: map[string]string{expected: "explicit replacement"}}
	svc := &Service{path: path, secrets: secrets}

	if got := svc.Load().LLM.APIKeyRef; got != expected {
		t.Fatalf("recovered API key ref = %q, want %q", got, expected)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Settings
	if err := json.Unmarshal(persisted, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.LLM.APIKeyRef != expected {
		t.Fatalf("persisted recovered ref = %q, want %q", decoded.LLM.APIKeyRef, expected)
	}
	if got := svc.Load().LLM.APIKeyRef; got != expected {
		t.Fatalf("second Load API key ref = %q, want %q", got, expected)
	}
	if secrets.setCalls != 0 || secrets.deleteCalls != 0 || len(secrets.replaceCalls) != 0 {
		t.Fatalf("metadata recovery mutated secrets: set=%d delete=%d replace=%v", secrets.setCalls, secrets.deleteCalls, secrets.replaceCalls)
	}
}

func TestLegacyLLMReplacementRemainsForwardCommittedWhenSettingsPersistFails(t *testing.T) {
	in := defaults()
	in.LLM.APIKeyRef = legacyLLMAPIKeyRef
	expected, err := llmCredentialRef(in.LLM)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	backup := filepath.Join(dir, "settings-before-fault.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var hookErr error
	secrets := &fakeSecretStore{values: map[string]string{legacyLLMAPIKeyRef: "legacy value"}}
	secrets.replaceHook = func() {
		if err := os.Rename(path, backup); err != nil {
			hookErr = err
			return
		}
		hookErr = os.Mkdir(path, 0o700)
	}
	svc := &Service{path: path, secrets: secrets}

	if err := svc.SetLLMAPIKey("explicit replacement"); err == nil {
		t.Fatal("SetLLMAPIKey unexpectedly succeeded after injected settings fault")
	}
	if hookErr != nil {
		t.Fatalf("inject settings persistence fault: %v", hookErr)
	}
	if _, found := secrets.values[legacyLLMAPIKeyRef]; found {
		t.Fatal("failed settings persistence rolled legacy ref back")
	}
	if got := secrets.values[expected]; got != "explicit replacement" {
		t.Fatalf("forward-committed scoped value = %q", got)
	}
	if secrets.setCalls != 0 || secrets.deleteCalls != 0 || len(secrets.replaceCalls) != 1 {
		t.Fatalf("unexpected compensation: set=%d delete=%d replace=%v", secrets.setCalls, secrets.deleteCalls, secrets.replaceCalls)
	}

	// Restore the unchanged pre-fault metadata to model restart after a process
	// crash between the two store publications. Startup must finish the forward
	// commit without copying or rewriting secret material.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	secrets.replaceHook = nil
	restarted := &Service{path: path, secrets: secrets}
	if got := restarted.Load().LLM.APIKeyRef; got != expected {
		t.Fatalf("restart recovery ref = %q, want %q", got, expected)
	}
	if secrets.setCalls != 0 || secrets.deleteCalls != 0 || len(secrets.replaceCalls) != 1 {
		t.Fatalf("restart recovery mutated secrets: set=%d delete=%d replace=%v", secrets.setCalls, secrets.deleteCalls, secrets.replaceCalls)
	}
}

func TestLegacyLLMQuarantineDoesNotAttemptScopedStoreWrite(t *testing.T) {
	secrets := &fakeSecretStore{
		values: map[string]string{legacyLLMAPIKeyRef: "legacy-key"},
		setErr: errors.New("keyring unavailable"),
	}
	svc := &Service{path: filepath.Join(t.TempDir(), "settings.json"), secrets: secrets}
	raw := []byte(`{"llm":{"provider":"custom","baseURL":"https://provider.example/v1","model":"model","apiKeyRef":"llm.apikey"}}`)
	if err := os.WriteFile(svc.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if ref := svc.Load().LLM.APIKeyRef; ref != legacyLLMAPIKeyRef {
		t.Fatalf("migration failure detached recoverable legacy ownership: %q", ref)
	}
	if secrets.values[legacyLLMAPIKeyRef] != "legacy-key" {
		t.Fatal("legacy credential was destroyed after failed migration")
	}
}

func TestLegacyLLMMigrationCheckedReadFailurePreservesOwnership(t *testing.T) {
	wantErr := errors.New("keyring temporarily unavailable")
	secrets := &fakeSecretStore{
		values: map[string]string{legacyLLMAPIKeyRef: "legacy-key"},
		getErr: wantErr,
	}
	svc := &Service{path: filepath.Join(t.TempDir(), "settings.json"), secrets: secrets}
	raw := []byte(`{"llm":{"provider":"custom","baseURL":"https://provider.example/v1","model":"model","apiKeyRef":"llm.apikey"}}`)
	if err := os.WriteFile(svc.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if ref := svc.Load().LLM.APIKeyRef; ref != legacyLLMAPIKeyRef {
		t.Fatalf("checked-read failure detached legacy ownership: %q", ref)
	}
	if secrets.values[legacyLLMAPIKeyRef] != "legacy-key" {
		t.Fatal("checked-read failure changed legacy credential evidence")
	}
}

func TestLLMCredentialPersistenceFailureRestoresSecret(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := llmCredentialRef(defaults().LLM)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &fakeSecretStore{values: map[string]string{ref: "old-key"}}
	svc := &Service{path: filepath.Join(blocker, "settings.json"), secrets: secrets}
	if err := svc.SetLLMAPIKey("new-key"); err == nil {
		t.Fatal("SetLLMAPIKey unexpectedly succeeded with an unwritable settings path")
	}
	if got := secrets.values[ref]; got != "old-key" {
		t.Fatalf("failed settings persistence left replacement credential %q", got)
	}
}

func TestUnhealthySecretStorePreservesOwnedLLMReferenceAndBlocksMutation(t *testing.T) {
	wantErr := errors.New("master key corrupt")
	in := defaults()
	ref, err := llmCredentialRef(in.LLM)
	if err != nil {
		t.Fatal(err)
	}
	in.LLM.APIKeyRef = ref
	raw, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := &fakeSecretStore{values: map[string]string{ref: "ciphertext-placeholder"}, healthErr: wantErr}
	svc := &Service{path: path, secrets: secrets}

	if got := svc.Load().LLM.APIKeyRef; got != ref {
		t.Fatalf("Load stripped owned ref %q while store was unhealthy; got %q", ref, got)
	}
	if err := svc.SetLLMAPIKey("replacement"); !errors.Is(err, wantErr) {
		t.Fatalf("SetLLMAPIKey error = %v, want health error", err)
	}
	if err := svc.DeleteLLMAPIKey(); !errors.Is(err, wantErr) {
		t.Fatalf("DeleteLLMAPIKey error = %v, want health error", err)
	}
	changed := in
	changed.LLM.BaseURL = "https://different.example/v1"
	if err := svc.Save(changed); !errors.Is(err, wantErr) {
		t.Fatalf("Save origin change error = %v, want health error", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("settings or purpose-bound ownership metadata changed while secret store was unhealthy")
	}
	status := svc.GetLLMAPIKeyStatus()
	if status.State != "unavailable" || !strings.Contains(status.Message, wantErr.Error()) {
		t.Fatalf("credential status = %+v, want unavailable with store diagnostic", status)
	}
}

func TestLLMAPIKeyStatusDistinguishesVerifiedQuarantinedAndMissing(t *testing.T) {
	writeSettings := func(t *testing.T, path, ref string) Settings {
		t.Helper()
		in := defaults()
		in.LLM = LLM{
			Provider:          "custom",
			BaseURL:           "https://provider.example/v1",
			Model:             "model",
			APIKeyRef:         ref,
			RequestTimeoutSec: DefaultRequestTimeoutSec,
		}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return in
	}

	t.Run("verified", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		in := writeSettings(t, path, "")
		ref, err := llmCredentialRef(in.LLM)
		if err != nil {
			t.Fatal(err)
		}
		writeSettings(t, path, ref)
		svc := &Service{path: path, secrets: &fakeSecretStore{values: map[string]string{ref: "key"}}}
		if got := svc.GetLLMAPIKeyStatus(); got.State != "verified" || got.Message != "" {
			t.Fatalf("credential status = %+v, want verified", got)
		}
		if !svc.HasLLMAPIKey() {
			t.Fatal("HasLLMAPIKey returned false for a verified credential")
		}
	})

	t.Run("quarantined", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		writeSettings(t, path, legacyLLMAPIKeyRef)
		svc := &Service{path: path, secrets: &fakeSecretStore{values: map[string]string{legacyLLMAPIKeyRef: "legacy"}}}
		if got := svc.GetLLMAPIKeyStatus(); got.State != "quarantined" || !strings.Contains(got.Message, "Re-enter") {
			t.Fatalf("credential status = %+v, want actionable quarantine", got)
		}
		if svc.HasLLMAPIKey() {
			t.Fatal("HasLLMAPIKey accepted a quarantined credential")
		}
	})

	t.Run("missing", func(t *testing.T) {
		svc := &Service{path: filepath.Join(t.TempDir(), "settings.json"), secrets: &fakeSecretStore{values: map[string]string{}}}
		if got := svc.GetLLMAPIKeyStatus(); got.State != "missing" || got.Message != "" {
			t.Fatalf("credential status = %+v, want missing", got)
		}
	})
}

func TestCorruptSettingsRemainInPlaceAndBlockEveryMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"theme":"dark","llm":`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := &fakeSecretStore{values: map[string]string{}}
	svc := &Service{path: path, secrets: secrets}

	if got := svc.Load(); got.Theme != defaults().Theme {
		t.Fatalf("Load on corrupt settings returned unexpected fallback: %+v", got)
	}
	if got := svc.LoadError(); !strings.Contains(got, "mutations are blocked") {
		t.Fatalf("LoadError = %q, want actionable latched diagnostic", got)
	}
	mutations := []struct {
		name string
		run  func() error
	}{
		{"save", func() error { return svc.Save(defaults()) }},
		{"remember workspace", func() error {
			_, err := svc.RememberWorkspace(filepath.Join(t.TempDir(), "workspace"))
			return err
		}},
		{"set API key", func() error { return svc.SetLLMAPIKey("replacement") }},
		{"delete API key", svc.DeleteLLMAPIKey},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if err := mutation.run(); err == nil || !strings.Contains(err.Error(), "left unchanged") {
				t.Fatalf("mutation error = %v, want latched corruption error", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, raw) {
				t.Fatalf("corrupt settings evidence changed after %s", mutation.name)
			}
		})
	}
	if _, err := os.Stat(path + ".corrupt"); !os.IsNotExist(err) {
		t.Fatalf("primary was moved aside despite fail-closed policy: %v", err)
	}
}

func TestOversizedSettingsRemainInPlaceAndBlockMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := bytes.Repeat([]byte(" "), maxSettingsBytes+1)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &Service{path: path}
	if _, err := svc.RememberWorkspace("workspace"); err == nil || !strings.Contains(err.Error(), "safety limit") {
		t.Fatalf("RememberWorkspace error = %v, want bounded-read error", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("oversized settings evidence was modified")
	}
}

func TestAmbiguousSettingsRemainInPlaceAndBlockMutation(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  []byte
	}{
		{
			name: "duplicate nested field",
			raw:  []byte(`{"llm":{"provider":"custom","baseURL":"https://trusted.example","BaseURL":"https://attacker.example"}}`),
		},
		{
			name: "unknown field",
			raw:  []byte(`{"theme":"dark","futureCredential":"opaque"}`),
		},
		{
			name: "null root",
			raw:  []byte(`null`),
		},
		{
			name: "invalid UTF-8",
			raw:  []byte{'{', '"', 't', 'h', 'e', 'm', 'e', '"', ':', '"', 0xff, '"', '}'},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, tt.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			svc := &Service{path: path}
			if err := svc.Save(defaults()); err == nil || !strings.Contains(err.Error(), "left unchanged") {
				t.Fatalf("Save error = %v, want latched ambiguous-settings error", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tt.raw) {
				t.Fatal("ambiguous settings evidence was modified")
			}
		})
	}
}

func TestLoadCapsRecentWorkspaceAmplification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	recents := make([]string, maxRecent+20)
	for i := range recents {
		recents[i] = fmt.Sprintf("workspace-%d", i)
	}
	raw, err := json.Marshal(Settings{RecentWorkspaces: recents})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &Service{path: path}
	if got := svc.Load().RecentWorkspaces; len(got) != maxRecent {
		t.Fatalf("recent workspace count = %d, want %d", len(got), maxRecent)
	}
}

func TestSaveRejectsStringsTheLoaderCannotSafelyRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name  string
		apply func(*Settings)
	}{
		{
			name: "oversized model",
			apply: func(in *Settings) {
				in.LLM.Model = strings.Repeat("m", maxLLMModelBytes+1)
			},
		},
		{
			name: "invalid UTF-8 workspace",
			apply: func(in *Settings) {
				in.LastWorkspace = string([]byte{0xff})
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{path: filepath.Join(t.TempDir(), "settings.json")}
			in := defaults()
			tt.apply(&in)
			if err := svc.Save(in); err == nil {
				t.Fatal("Save accepted settings that cannot round-trip safely")
			}
			if _, err := os.Stat(svc.path); !os.IsNotExist(err) {
				t.Fatalf("unsafe settings were published: %v", err)
			}
		})
	}
}
