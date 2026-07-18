package main

import (
	"testing"

	"novera/internal/settings"
)

type fakeBoundSecretStore struct {
	values map[string]string
}

func (f *fakeBoundSecretStore) Get(ref string) (string, bool) {
	v, ok := f.values[ref]
	return v, ok
}

func (f *fakeBoundSecretStore) Set(ref, value string) error {
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[ref] = value
	return nil
}

func (f *fakeBoundSecretStore) Has(ref string) bool {
	_, ok := f.values[ref]
	return ok
}

func (f *fakeBoundSecretStore) Delete(ref string) error {
	delete(f.values, ref)
	return nil
}

func TestSecretServiceCannotAddressForeignCredentialRefs(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("APPDATA", configDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	store := &fakeBoundSecretStore{values: map[string]string{
		"db.cred.profile-a": "db-password",
	}}
	set := settings.New(store)
	config := set.Load()
	config.LLM.Provider = "custom"
	config.LLM.BaseURL = "https://provider.example/v1"
	config.LLM.Model = "model"
	if err := set.Save(config); err != nil {
		t.Fatal(err)
	}
	svc := &SecretService{settings: set}

	if err := svc.SetKey("db.cred.profile-a", "replacement"); err == nil {
		t.Fatal("renderer-facing service accepted a database credential ref")
	}
	if svc.HasKey("db.cred.profile-a") {
		t.Fatal("renderer-facing service disclosed foreign credential existence")
	}
	if err := svc.DeleteKey("db.cred.profile-a"); err == nil {
		t.Fatal("renderer-facing service deleted a database credential ref")
	}
	if got := store.values["db.cred.profile-a"]; got != "db-password" {
		t.Fatalf("foreign credential changed to %q", got)
	}

	if err := svc.SetKey(legacyLLMKeySelector, "llm-key"); err != nil {
		t.Fatal(err)
	}
	refs := svc.ListKeys()
	if len(refs) != 1 || refs[0] != legacyLLMKeySelector {
		t.Fatalf("ListKeys exposed the wrong ownership surface: %v", refs)
	}
	if got := store.values["db.cred.profile-a"]; got != "db-password" {
		t.Fatalf("DB credential changed while setting LLM key: %q", got)
	}
}
