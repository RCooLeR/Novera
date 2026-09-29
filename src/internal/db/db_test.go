package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/persistfile"
)

type fakeSecretStore struct {
	values       map[string]string
	getRefs      []string
	healthErr    error
	setErr       error
	replaceErr   error
	setCalls     int
	deleteCalls  int
	replaceCalls [][2]string
	replaceHook  func()
}

type checkedFakeSecretStore struct {
	*fakeSecretStore
	checkedValue string
	checkedFound bool
	checkedErr   error
}

type checkedManyFakeSecretStore struct {
	*fakeSecretStore
	checkedValues map[string]string
	checkedErr    error
	checkedRefs   [][]string
}

func foreignLLMRefForTest() string {
	return strings.Join([]string{"llm", "apikey", "v1", "foreign"}, ".")
}

func (f *checkedFakeSecretStore) GetChecked(ref string) (string, bool, error) {
	f.getRefs = append(f.getRefs, ref)
	return f.checkedValue, f.checkedFound, f.checkedErr
}

func (f *checkedManyFakeSecretStore) GetManyChecked(refs []string) (map[string]string, error) {
	f.checkedRefs = append(f.checkedRefs, append([]string(nil), refs...))
	if f.checkedErr != nil {
		return nil, f.checkedErr
	}
	if f.healthErr != nil {
		return nil, f.healthErr
	}
	return f.checkedValues, nil
}

func (f *fakeSecretStore) Health() error { return f.healthErr }

func (f *fakeSecretStore) Get(ref string) (string, bool) {
	f.getRefs = append(f.getRefs, ref)
	v, ok := f.values[ref]
	return v, ok
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
		return errors.New("old secret ref does not exist")
	}
	delete(f.values, oldRef)
	f.values[newRef] = value
	if f.replaceHook != nil {
		f.replaceHook()
	}
	return nil
}

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"":               true,
		"localhost":      true,
		"LocalHost":      true,
		"127.0.0.1":      true,
		"127.0.0.5":      true,
		"::1":            true,
		"[::1]":          true,
		"db.example.com": false,
		"10.0.0.5":       false,
		"192.168.1.10":   false,
		"0.0.0.0":        false,
	}
	for host, want := range cases {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestDSNPostgresSecureByDefault(t *testing.T) {
	// Network host with no explicit mode must authenticate the remote server, not
	// merely encrypt traffic with sslmode=require (which skips verification when
	// no root certificate is configured in pgx).
	_, dsn, err := dsnFor(Profile{Kind: "postgres", Host: "db.example.com", Database: "app", User: "u"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "sslmode=verify-full") {
		t.Errorf("network postgres should default to sslmode=verify-full, got %q", dsn)
	}

	// Loopback keeps the lenient `prefer` so local servers without TLS still work.
	_, dsn, err = dsnFor(Profile{Kind: "postgres", Host: "localhost", Database: "app", User: "u"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "sslmode=prefer") {
		t.Errorf("loopback postgres should default to sslmode=prefer, got %q", dsn)
	}

	// An explicit mode always wins, even on a network host.
	_, dsn, err = dsnFor(Profile{Kind: "postgres", Host: "db.example.com", Database: "app", User: "u", SSLMode: "disable"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "sslmode=disable") {
		t.Errorf("explicit sslmode must be honored, got %q", dsn)
	}
}

func TestDSNPostgresInsecureRemoteOptOutsAreLogged(t *testing.T) {
	var logs bytes.Buffer
	oldWriter, oldFlags, oldPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})

	cases := map[string]string{
		"disable":   "UNENCRYPTED",
		"allow":     "UNENCRYPTED",
		"prefer":    "may fall back to UNENCRYPTED",
		"require":   "NOT VERIFIED",
		"verify-ca": "hostname is NOT VERIFIED",
	}
	for mode, warning := range cases {
		logs.Reset()
		if _, _, err := dsnFor(Profile{Kind: "postgres", Host: "db.example.com", Database: "app", User: "u", SSLMode: mode}, "secret"); err != nil {
			t.Fatalf("dsnFor sslmode=%s: %v", mode, err)
		}
		if got := logs.String(); !strings.Contains(got, warning) {
			t.Errorf("sslmode=%s log = %q, want warning containing %q", mode, got, warning)
		}
	}
}

func TestDSNMySQLSecureByDefault(t *testing.T) {
	// Network host with no explicit mode must encrypt and authenticate the server.
	_, dsn, err := dsnFor(Profile{Kind: "mysql", Host: "db.example.com", Database: "app", User: "u"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "tls=true") {
		t.Errorf("network mysql should default to verified TLS, got %q", dsn)
	}

	// Loopback stays plaintext-friendly (no tls param forces a default that would
	// break local servers without TLS).
	_, dsn, err = dsnFor(Profile{Kind: "mysql", Host: "127.0.0.1", Database: "app", User: "u"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if contains(dsn, "tls=") {
		t.Errorf("loopback mysql should not force TLS, got %q", dsn)
	}

	// Explicit verify mode maps to full verification.
	_, dsn, err = dsnFor(Profile{Kind: "mysql", Host: "db.example.com", Database: "app", User: "u", SSLMode: "verify-full"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "tls=true") {
		t.Errorf("verify-full mysql should map to tls=true, got %q", dsn)
	}

	// Unverified TLS remains available only as an explicit compatibility opt-out.
	_, dsn, err = dsnFor(Profile{Kind: "mysql", Host: "db.example.com", Database: "app", User: "u", SSLMode: "require"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "tls=skip-verify") {
		t.Errorf("explicit require should map to unverified TLS compatibility mode, got %q", dsn)
	}
}

func TestSQLiteQueryIsReadOnlyAndLimited(t *testing.T) {
	path := seedSQLite(t)
	s := &Service{profiles: []Profile{{ID: "local", Name: "Local", Kind: "sqlite", File: path}}}

	got, err := s.Query("local", "SELECT id, name FROM users ORDER BY id", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.RowCount != 1 || !got.Truncated {
		t.Fatalf("RowCount=%d Truncated=%v, want 1/true", got.RowCount, got.Truncated)
	}
	if got.TruncationReason != queryTruncatedRows {
		t.Fatalf("TruncationReason=%q, want %q", got.TruncationReason, queryTruncatedRows)
	}
	if len(got.Rows) != 1 || got.Rows[0][1] != "Ada" {
		t.Fatalf("unexpected rows: %+v", got.Rows)
	}

	if _, err := s.Query("local", "DELETE FROM users", 10); err == nil {
		t.Fatal("DELETE should be rejected by read-only query guard")
	}
	if _, err := s.Query("local", "SELECT id FROM users; DROP TABLE users;", 10); err == nil {
		t.Fatal("multi-statement query should be rejected")
	}

	after, err := s.Query("local", "SELECT count(*) FROM users", 10)
	if err != nil {
		t.Fatal(err)
	}
	if after.Rows[0][0] != "2" {
		t.Fatalf("table changed after rejected writes, count=%q", after.Rows[0][0])
	}
}

func TestSQLiteMetadataAndPreviewPreserveAdversarialIdentifiers(t *testing.T) {
	path := seedSQLite(t)
	s := &Service{profiles: []Profile{{ID: "local", Name: "Local", Kind: "sqlite", File: path}}}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	const specialTable = `2026 data "quoted" Ω`
	if _, err := conn.Exec(`CREATE TABLE "2026 data ""quoted"" Ω" ("odd column" TEXT, "select" INTEGER)`); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO "2026 data ""quoted"" Ω" VALUES ('safe', 7)`); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	tables, err := s.ListTables("local")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, table := range tables {
		if table.Name == specialTable {
			found = true
			if table.Schema != "main" || table.Type != "table" {
				t.Fatalf("special table identity = %+v", table)
			}
		}
	}
	if !found {
		t.Fatalf("special table missing from metadata: %+v", tables)
	}

	cols, err := s.ListColumns("local", "main", specialTable)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || cols[0].Name != "odd column" || cols[1].Name != "select" {
		t.Fatalf("columns = %+v, want exact adversarial names", cols)
	}
	query, err := s.BuildTableQuery("local", "main", specialTable, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := `SELECT * FROM "main"."2026 data ""quoted"" Ω" LIMIT 100`
	if query != wantQuery {
		t.Fatalf("BuildTableQuery = %q, want %q", query, wantQuery)
	}
	result, err := s.Query("local", query, 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || result.Rows[0][0] != "safe" || result.Rows[0][1] != "7" {
		t.Fatalf("preview result = %+v", result)
	}

	// The hostile-looking value remains bound metadata input. It cannot append
	// a statement and does not affect the existing users table.
	cols, err = s.ListColumns("local", "main", `users"; DROP TABLE users; --`)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 0 {
		t.Fatalf("nonexistent hostile-looking table returned columns: %+v", cols)
	}
	after, err := s.Query("local", "SELECT count(*) FROM users", 10)
	if err != nil || after.Rows[0][0] != "2" {
		t.Fatalf("bound metadata input affected users: result=%+v err=%v", after, err)
	}
	if _, err := s.ListColumns("local", "temp", "users"); err == nil {
		t.Fatal("SQLite metadata accepted a schema not exposed by ListTables")
	}
}

func TestBuildTableQueryQuotesEachDialect(t *testing.T) {
	s := &Service{profiles: []Profile{
		{ID: "pg", Kind: "postgres"},
		{ID: "my", Kind: "mysql"},
	}}

	pg, err := s.BuildTableQuery("pg", `tenant"east`, `orders"; DROP TABLE audit; --`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := `SELECT * FROM "tenant""east"."orders""; DROP TABLE audit; --" LIMIT 100`; pg != want {
		t.Fatalf("postgres query = %q, want %q", pg, want)
	}
	mysql, err := s.BuildTableQuery("my", "tenant`east", "orders`; DROP TABLE audit; --", maxRowLimit+1)
	if err != nil {
		t.Fatal(err)
	}
	if want := "SELECT * FROM `tenant``east`.`orders``; DROP TABLE audit; --` LIMIT 5000"; mysql != want {
		t.Fatalf("mysql query = %q, want %q", mysql, want)
	}
}

func TestQueryRejectsExcessiveColumnsAndCells(t *testing.T) {
	path := seedSQLite(t)
	s := &Service{profiles: []Profile{{ID: "local", Kind: "sqlite", File: path}}}

	tooManyColumns := "SELECT " + strings.TrimSuffix(strings.Repeat("1,", maxQueryColumns+1), ",")
	if _, err := s.Query("local", tooManyColumns, 10); err == nil || !strings.Contains(err.Error(), "columns") {
		t.Fatalf("excessive-column error = %v", err)
	}
	if _, err := s.Query("local", "SELECT zeroblob(1048577)", 10); err == nil || !strings.Contains(err.Error(), "1048576-byte limit") {
		t.Fatalf("oversized-cell error = %v", err)
	}
}

func TestQueryTruncatesAtSerializedByteBudget(t *testing.T) {
	path := seedSQLite(t)
	s := &Service{profiles: []Profile{{ID: "local", Kind: "sqlite", File: path}}}

	got, err := s.Query("local", "SELECT hex(zeroblob(350000)) AS payload FROM users ORDER BY id", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.RowCount != 1 || len(got.Rows) != 1 || !got.Truncated || got.TruncationReason != queryTruncatedBytes {
		t.Fatalf("byte-budget result = rows:%d truncated:%v reason:%q", got.RowCount, got.Truncated, got.TruncationReason)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > int(maxQueryResultBytes) {
		t.Fatalf("serialized result is %d bytes, limit is %d", len(raw), maxQueryResultBytes)
	}
}

func TestSaveProfileIgnoresCallerSecretRefAndBindsPasswordToOrigin(t *testing.T) {
	foreignRef := foreignLLMRefForTest()
	secrets := &fakeSecretStore{values: map[string]string{foreignRef: "llm-key"}}
	svc := &Service{
		path:     filepath.Join(t.TempDir(), "profiles.json"),
		profiles: []Profile{},
		secrets:  secrets,
	}
	foreignOnly, err := svc.SaveProfile(Profile{
		Name:      "Foreign ref attempt",
		Kind:      "postgres",
		Host:      "attacker.example",
		User:      "reader",
		SecretRef: foreignRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if foreignOnly.SecretRef != "" {
		t.Fatalf("new DB profile attached caller-selected LLM ref: %q", foreignOnly.SecretRef)
	}

	created, err := svc.SaveProfile(Profile{
		Name:      "Production",
		Kind:      "postgres",
		Host:      "trusted.example",
		Database:  "app",
		User:      "reader",
		SecretRef: foreignRef, // adversarial input
		Password:  "db-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := dbCredentialRef(created)
	if created.SecretRef != expected || secrets.values[expected] != "db-password" {
		t.Fatalf("created credential = ref %q values %v, want owned ref %q", created.SecretRef, secrets.values, expected)
	}
	if secrets.values[foreignRef] != "llm-key" {
		t.Fatal("LLM credential was overwritten by DB profile save")
	}

	// Carrying the old ref (or a foreign ref) while changing the destination must
	// not send the existing password to that destination.
	changed := created
	changed.Host = "attacker.example"
	changed.SecretRef = expected
	changed.Password = ""
	if _, err = svc.SaveProfile(changed); err == nil {
		t.Fatal("destination change without password re-entry was accepted")
	}
	if got := svc.profiles[len(svc.profiles)-1]; got.Host != "trusted.example" || got.SecretRef != expected {
		t.Fatalf("rejected destination change mutated profile: %+v", got)
	}
	if secrets.values[expected] != "db-password" {
		t.Fatalf("rejected destination change modified old credential %q", secrets.values[expected])
	}

	changed.Password = "new-db-password" // explicit re-entry binds a new scoped key
	changed, err = svc.SaveProfile(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changed.SecretRef == "" || changed.SecretRef == expected || secrets.values[changed.SecretRef] != "new-db-password" {
		t.Fatalf("re-entered password was not bound to new scope: profile=%+v secrets=%v", changed, secrets.values)
	}
	if _, ok := secrets.values[expected]; ok {
		t.Fatalf("old scoped DB credential %q remained after explicit rebinding", expected)
	}
}

func TestSaveProfileRejectsCallerIssuedIDAndCrossProfileRef(t *testing.T) {
	secrets := &fakeSecretStore{values: map[string]string{}}
	svc := &Service{path: filepath.Join(t.TempDir(), "profiles.json"), profiles: []Profile{}, secrets: secrets}
	if _, err := svc.SaveProfile(Profile{
		ID:        "caller-chosen",
		Name:      "Bad",
		Kind:      "postgres",
		Host:      "attacker.example",
		SecretRef: "db.cred.someone-else",
	}); err == nil {
		t.Fatal("caller-issued profile ID was accepted")
	}
	if err := svc.DeleteProfile("missing"); err == nil {
		t.Fatal("deleting a missing profile should return not found")
	}

	a, err := svc.SaveProfile(Profile{Name: "A", Kind: "postgres", Host: "a.example", User: "a", Password: "a-key"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.SaveProfile(Profile{Name: "B", Kind: "postgres", Host: "b.example", User: "b", Password: "b-key"})
	if err != nil {
		t.Fatal(err)
	}
	a.SecretRef = b.SecretRef // attempt to attach B's credential to A
	a.Password = ""
	updated, err := svc.SaveProfile(a)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SecretRef != dbCredentialRef(a) || updated.SecretRef == b.SecretRef {
		t.Fatalf("cross-profile ref accepted: A=%q B=%q", updated.SecretRef, b.SecretRef)
	}
}

func TestLoadQuarantinesOnlyExactLegacyDBProfileRef(t *testing.T) {
	secrets := &fakeSecretStore{values: map[string]string{
		"db.cred.profile-a": "a-key",
		"llm.apikey":        "llm-key",
	}}
	path := filepath.Join(t.TempDir(), "profiles.json")
	raw := []byte(`[
  {"id":"profile-a","name":"A","kind":"postgres","host":"a.example","user":"a","secretRef":"db.cred.profile-a"},
  {"id":"profile-b","name":"B","kind":"postgres","host":"b.example","user":"b","secretRef":"llm.apikey"}
]`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &Service{path: path, profiles: []Profile{}, secrets: secrets}
	svc.load()
	if len(svc.profiles) != 2 {
		t.Fatalf("profiles = %d, want 2", len(svc.profiles))
	}
	expected := dbCredentialRef(svc.profiles[0])
	if svc.profiles[0].SecretRef != legacyDBCredentialRef("profile-a") {
		t.Fatalf("legacy DB ownership was not preserved in quarantine: profile %+v secrets %v", svc.profiles[0], secrets.values)
	}
	if _, rebound := secrets.values[expected]; rebound {
		t.Fatal("legacy DB credential was automatically rebound to editable connection metadata")
	}
	if svc.profiles[1].SecretRef != "" {
		t.Fatalf("foreign LLM ref was attached to DB profile: %q", svc.profiles[1].SecretRef)
	}
	if secrets.values["llm.apikey"] != "llm-key" {
		t.Fatal("foreign LLM secret was modified during DB profile sanitation")
	}
}

func TestProfileLoadFailuresPreserveEvidenceAndBlockMutations(t *testing.T) {
	tests := []struct {
		name string
		raw  func(t *testing.T) []byte
	}{
		{
			name: "corrupt JSON",
			raw: func(t *testing.T) []byte {
				return []byte(`[{"id":"profile-a"`)
			},
		},
		{
			name: "oversized file",
			raw: func(t *testing.T) []byte {
				return bytes.Repeat([]byte{'x'}, maxProfilesSize+1)
			},
		},
		{
			name: "too many profiles",
			raw: func(t *testing.T) []byte {
				profiles := make([]Profile, maxProfiles+1)
				for i := range profiles {
					profiles[i] = Profile{ID: "p", Name: "P", Kind: "sqlite", File: `C:\data.db`}
				}
				b, err := json.Marshal(profiles)
				if err != nil {
					t.Fatal(err)
				}
				if len(b) > maxProfilesSize {
					t.Fatalf("semantic-limit fixture unexpectedly exceeds byte limit: %d", len(b))
				}
				return b
			},
		},
		{
			name: "duplicate profile ids",
			raw: func(t *testing.T) []byte {
				return []byte(`[{"id":"same","name":"A","kind":"sqlite"},{"id":"same","name":"B","kind":"sqlite"}]`)
			},
		},
		{
			name: "duplicate JSON key",
			raw: func(t *testing.T) []byte {
				return []byte(`[{"id":"profile-a","name":"A","kind":"sqlite","secretRef":"safe","SecretRef":"foreign"}]`)
			},
		},
		{
			name: "persisted plaintext password",
			raw: func(t *testing.T) []byte {
				return []byte(`[{"id":"profile-a","name":"A","kind":"postgres","password":"plaintext-evidence"}]`)
			},
		},
		{
			name: "unknown profile field",
			raw: func(t *testing.T) []byte {
				return []byte(`[{"id":"profile-a","name":"A","kind":"sqlite","futureCredential":"opaque"}]`)
			},
		},
		{
			name: "invalid UTF-8",
			raw: func(t *testing.T) []byte {
				return []byte{'[', '{', '"', 'i', 'd', '"', ':', '"', 0xff, '"', '}', ']'}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.json")
			raw := tc.raw(t)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			svc := &Service{path: path, profiles: []Profile{}, secrets: &fakeSecretStore{values: map[string]string{}}}
			svc.load()
			if svc.LoadError() == "" {
				t.Fatal("unsafe profile file did not latch a load error")
			}
			if strings.Contains(strings.ToLower(svc.LoadError()), "backed up") {
				t.Fatalf("load error falsely claims a backup: %q", svc.LoadError())
			}
			if _, err := svc.SaveProfile(Profile{Name: "Replacement", Kind: "sqlite", File: `C:\replacement.db`}); err == nil {
				t.Fatal("SaveProfile was allowed while the load error was latched")
			}
			if err := svc.DeleteProfile("profile-a"); err == nil {
				t.Fatal("DeleteProfile was allowed while the load error was latched")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, raw) {
				t.Fatal("unsafe profile evidence changed after blocked mutations")
			}
			if _, err := os.Stat(path + ".corrupt"); !os.IsNotExist(err) {
				t.Fatalf("unexpected renamed backup exists or stat failed: %v", err)
			}
		})
	}
}

func TestSaveProfileCannotPublishAFileTheLoaderWouldReject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	svc := &Service{path: path, profiles: []Profile{}}
	if _, err := svc.SaveProfile(Profile{
		Name: strings.Repeat("x", maxProfilesSize),
		Kind: "sqlite",
		File: `C:\large.db`,
	}); err == nil {
		t.Fatal("SaveProfile published a profile file above the loader's byte limit")
	}
	if len(svc.profiles) != 0 {
		t.Fatalf("failed oversized save leaked into memory: %+v", svc.profiles)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversized profile file was created or stat failed: %v", err)
	}
}

func TestSaveProfileRejectsInvalidUTF8BeforeSecretOrDiskMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	secrets := &fakeSecretStore{values: map[string]string{}}
	svc := &Service{path: path, profiles: []Profile{}, secrets: secrets}
	if _, err := svc.SaveProfile(Profile{
		Name:     string([]byte{0xff}),
		Kind:     "postgres",
		Host:     "db.example",
		Password: "must-not-be-stored",
	}); err == nil {
		t.Fatal("SaveProfile accepted invalid UTF-8")
	}
	if len(secrets.values) != 0 {
		t.Fatalf("rejected profile changed secrets: %v", secrets.values)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected profile was published: %v", err)
	}
}

func TestUnhealthySecretStorePreservesExactLegacyOwnership(t *testing.T) {
	original := Profile{ID: "profile-a", Name: "A", Kind: "postgres", Host: "db.example", User: "reader"}
	original.SecretRef = legacyDBCredentialRef(original.ID)
	other := Profile{ID: "profile-b", Name: "B", Kind: "sqlite", File: `C:\other.db`}
	raw, err := json.MarshalIndent([]Profile{original, other}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("secret ciphertext corrupt")
	secrets := &fakeSecretStore{
		values:    map[string]string{original.SecretRef: "ciphertext-evidence"},
		healthErr: wantErr,
	}
	svc := &Service{path: path, profiles: []Profile{}, secrets: secrets}
	svc.load()
	if svc.LoadError() != "" {
		t.Fatalf("unhealthy secret store should preserve owned metadata without treating profile JSON as corrupt: %v", svc.LoadError())
	}
	if len(svc.profiles) != 2 || svc.profiles[0].SecretRef != original.SecretRef || svc.profiles[1] != other {
		t.Fatalf("legacy owned reference was detached while storage was unhealthy: %+v", svc.profiles)
	}

	update := original
	update.Name = "Attempted update"
	if _, err := svc.SaveProfile(update); !errors.Is(err, wantErr) {
		t.Fatalf("SaveProfile error = %v, want wrapped health error", err)
	}
	if err := svc.DeleteProfile(original.ID); !errors.Is(err, wantErr) {
		t.Fatalf("DeleteProfile error = %v, want wrapped health error", err)
	}
	if len(svc.profiles) != 2 || svc.profiles[0] != original || svc.profiles[1] != other {
		t.Fatalf("blocked mutation changed in-memory profile evidence: %+v", svc.profiles)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("legacy profile ownership evidence changed while secret storage was unhealthy")
	}
	if got := secrets.values[original.SecretRef]; got != "ciphertext-evidence" {
		t.Fatalf("legacy credential evidence changed to %q", got)
	}
}

func TestRecoveredLegacyCredentialCannotBeSilentlyDetachedOnProfileEdit(t *testing.T) {
	original := Profile{ID: "profile-a", Name: "A", Kind: "postgres", Host: "db.example", User: "reader"}
	original.SecretRef = legacyDBCredentialRef(original.ID)
	secrets := &fakeSecretStore{values: map[string]string{original.SecretRef: "legacy-password"}}
	svc := &Service{
		path:     filepath.Join(t.TempDir(), "profiles.json"),
		profiles: []Profile{original},
		secrets:  secrets,
	}
	updated := original
	updated.Name = "Renamed"
	if _, err := svc.SaveProfile(updated); err == nil || !strings.Contains(err.Error(), "legacy reference") {
		t.Fatalf("SaveProfile error = %v, want explicit legacy-migration refusal", err)
	}
	if svc.profiles[0] != original || secrets.values[original.SecretRef] != "legacy-password" {
		t.Fatalf("blocked legacy edit changed ownership evidence: profile=%+v secrets=%v", svc.profiles[0], secrets.values)
	}
	if _, err := os.Stat(svc.path); !os.IsNotExist(err) {
		t.Fatalf("blocked legacy edit unexpectedly wrote a profile file: %v", err)
	}

	updated.Password = "re-entered-password"
	secrets.values[dbCredentialRef(updated)] = "stale destination password"
	saved, err := svc.SaveProfile(updated)
	if err != nil {
		t.Fatal(err)
	}
	if saved.SecretRef != dbCredentialRef(saved) || secrets.values[saved.SecretRef] != "re-entered-password" {
		t.Fatalf("explicit re-entry was not bound to the scoped ref: profile=%+v secrets=%v", saved, secrets.values)
	}
	if _, exists := secrets.values[original.SecretRef]; exists {
		t.Fatal("legacy credential remained after successful explicit rebinding")
	}
	if len(secrets.replaceCalls) != 1 || secrets.replaceCalls[0] != [2]string{original.SecretRef, saved.SecretRef} {
		t.Fatalf("legacy Replace calls = %#v", secrets.replaceCalls)
	}
	if secrets.setCalls != 0 || secrets.deleteCalls != 0 {
		t.Fatalf("legacy replacement used Set/Delete: set=%d delete=%d", secrets.setCalls, secrets.deleteCalls)
	}
}

func TestCheckedReadFailureDuringLoadPreservesCredentialOwnership(t *testing.T) {
	original := Profile{ID: "profile-a", Name: "A", Kind: "postgres", Host: "db.example", User: "reader"}
	original.SecretRef = legacyDBCredentialRef(original.ID)
	raw, err := json.MarshalIndent([]Profile{original}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("keyring temporarily unavailable")
	base := &fakeSecretStore{values: map[string]string{original.SecretRef: "legacy-password"}}
	secrets := &checkedFakeSecretStore{fakeSecretStore: base, checkedErr: wantErr}
	svc := &Service{path: path, profiles: []Profile{}, secrets: secrets}
	svc.load()
	if svc.LoadError() != "" {
		t.Fatalf("transient credential read was treated as profile corruption: %s", svc.LoadError())
	}
	if len(svc.profiles) != 1 || svc.profiles[0].SecretRef != original.SecretRef {
		t.Fatalf("checked-read failure detached ownership: %+v", svc.profiles)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("checked-read failure rewrote profile ownership evidence")
	}
}

func TestLegacyMigrationStoreFailurePreservesRecoverableOwnership(t *testing.T) {
	original := Profile{ID: "profile-a", Name: "A", Kind: "postgres", Host: "db.example", User: "reader"}
	original.SecretRef = legacyDBCredentialRef(original.ID)
	raw, err := json.MarshalIndent([]Profile{original}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("secret store already contains the maximum number of entries")
	secrets := &fakeSecretStore{
		values: map[string]string{original.SecretRef: "legacy-password"},
		setErr: wantErr,
	}
	svc := &Service{path: path, profiles: []Profile{}, secrets: secrets}
	svc.load()
	if svc.LoadError() != "" {
		t.Fatalf("recoverable migration failure was treated as profile corruption: %s", svc.LoadError())
	}
	if len(svc.profiles) != 1 || svc.profiles[0].SecretRef != original.SecretRef {
		t.Fatalf("migration failure detached legacy ownership: %+v", svc.profiles)
	}
	if got := secrets.values[original.SecretRef]; got != "legacy-password" {
		t.Fatalf("migration failure changed legacy credential evidence to %q", got)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("migration failure rewrote profile ownership evidence")
	}
}

func TestLoadChecksCredentialOwnershipInOneSecretSnapshot(t *testing.T) {
	exact := Profile{ID: "profile-a", Name: "A", Kind: "postgres", Host: "a.example", User: "reader"}
	exact.SecretRef = dbCredentialRef(exact)
	legacy := Profile{ID: "profile-b", Name: "B", Kind: "postgres", Host: "b.example", User: "reader"}
	legacy.SecretRef = legacyDBCredentialRef(legacy.ID)
	raw, err := json.MarshalIndent([]Profile{exact, legacy}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	base := &fakeSecretStore{values: map[string]string{legacy.SecretRef: "legacy-password"}}
	secrets := &checkedManyFakeSecretStore{
		fakeSecretStore: base,
		checkedValues: map[string]string{
			exact.SecretRef:  "exact-password",
			legacy.SecretRef: "legacy-password",
		},
	}
	svc := &Service{path: path, profiles: []Profile{}, secrets: secrets}
	svc.load()
	if svc.LoadError() != "" {
		t.Fatalf("load error = %q", svc.LoadError())
	}
	if len(secrets.checkedRefs) != 1 {
		t.Fatalf("GetManyChecked calls = %d, want one", len(secrets.checkedRefs))
	}
	wantRefs := map[string]bool{
		exact.SecretRef:         true,
		legacy.SecretRef:        true,
		dbCredentialRef(legacy): true,
	}
	if len(secrets.checkedRefs[0]) != len(wantRefs) {
		t.Fatalf("GetManyChecked refs = %v, want owned and recovery refs", secrets.checkedRefs[0])
	}
	for _, ref := range secrets.checkedRefs[0] {
		if !wantRefs[ref] {
			t.Fatalf("GetManyChecked received unexpected ref %q", ref)
		}
	}
	if len(base.getRefs) != 0 {
		t.Fatalf("loader fell back to per-ref reads after bulk validation: %v", base.getRefs)
	}
	if svc.profiles[0].SecretRef != exact.SecretRef {
		t.Fatalf("exact scoped ref changed to %q", svc.profiles[0].SecretRef)
	}
	if got := svc.profiles[1].SecretRef; got != legacy.SecretRef {
		t.Fatalf("bulk snapshot changed quarantined legacy ref to %q", got)
	}
}

func TestLoadRecoversInterruptedExplicitLegacyProfileReplacement(t *testing.T) {
	profile := Profile{ID: "profile-a", Name: "A", Kind: "postgres", Host: "db.example", User: "reader"}
	profile.SecretRef = legacyDBCredentialRef(profile.ID)
	expected := dbCredentialRef(profile)
	raw, err := json.MarshalIndent([]Profile{profile}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	base := &fakeSecretStore{values: map[string]string{expected: "explicit replacement"}}
	secrets := &checkedManyFakeSecretStore{
		fakeSecretStore: base,
		checkedValues:   map[string]string{expected: "explicit replacement"},
	}
	svc := &Service{path: path, profiles: []Profile{}, secrets: secrets}

	svc.load()
	if svc.LoadError() != "" {
		t.Fatalf("load error = %q", svc.LoadError())
	}
	if len(svc.profiles) != 1 || svc.profiles[0].SecretRef != expected {
		t.Fatalf("recovered profiles = %+v, want ref %q", svc.profiles, expected)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []Profile
	if err := json.Unmarshal(persisted, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].SecretRef != expected {
		t.Fatalf("persisted recovered profiles = %+v", decoded)
	}
	if len(secrets.checkedRefs) != 1 || len(secrets.checkedRefs[0]) != 2 {
		t.Fatalf("recovery snapshot refs = %#v, want legacy and exact scoped refs", secrets.checkedRefs)
	}

	// A second startup/load sees ordinary scoped ownership and performs no
	// secret migration or replacement.
	svc.load()
	if len(svc.profiles) != 1 || svc.profiles[0].SecretRef != expected {
		t.Fatalf("second load profiles = %+v", svc.profiles)
	}
	if base.setCalls != 0 || base.deleteCalls != 0 || len(base.replaceCalls) != 0 {
		t.Fatalf("metadata recovery mutated secrets: set=%d delete=%d replace=%v", base.setCalls, base.deleteCalls, base.replaceCalls)
	}
}

func TestLegacyProfileReplacementRemainsForwardCommittedWhenProfilePersistFails(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := Profile{ID: "profile-a", Name: "Original", Kind: "postgres", Host: "db.example", User: "reader"}
	original.SecretRef = legacyDBCredentialRef(original.ID)
	update := original
	update.Name = "Updated"
	update.Password = "explicit replacement"
	expected := dbCredentialRef(update)
	secrets := &fakeSecretStore{values: map[string]string{
		original.SecretRef: "legacy password",
		expected:           "stale destination password",
	}}
	svc := &Service{
		path:     filepath.Join(blocker, "profiles.json"),
		profiles: []Profile{original},
		secrets:  secrets,
	}

	if _, err := svc.SaveProfile(update); err == nil {
		t.Fatal("SaveProfile unexpectedly succeeded with an unwritable profile path")
	}
	if len(svc.profiles) != 1 || svc.profiles[0] != original {
		t.Fatalf("failed profile commit changed memory: %+v", svc.profiles)
	}
	if _, found := secrets.values[original.SecretRef]; found {
		t.Fatal("failed profile persistence rolled the legacy secret back")
	}
	if got := secrets.values[expected]; got != "explicit replacement" {
		t.Fatalf("forward-committed scoped password = %q", got)
	}
	if secrets.setCalls != 0 || secrets.deleteCalls != 0 || len(secrets.replaceCalls) != 1 {
		t.Fatalf("unexpected compensation: set=%d delete=%d replace=%v", secrets.setCalls, secrets.deleteCalls, secrets.replaceCalls)
	}

	// Model restart with the unchanged old profile metadata. The exact scoped
	// secret is enough to finish the interrupted forward commit without a
	// plaintext copy or another secret-store mutation.
	recoveryPath := filepath.Join(t.TempDir(), "profiles.json")
	raw, err := json.MarshalIndent([]Profile{original}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recoveryPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := &Service{path: recoveryPath, profiles: []Profile{}, secrets: secrets}
	restarted.load()
	if restarted.LoadError() != "" {
		t.Fatalf("restart load error = %q", restarted.LoadError())
	}
	if len(restarted.profiles) != 1 || restarted.profiles[0].SecretRef != expected {
		t.Fatalf("restart recovery profiles = %+v, want ref %q", restarted.profiles, expected)
	}
	if secrets.setCalls != 0 || secrets.deleteCalls != 0 || len(secrets.replaceCalls) != 1 {
		t.Fatalf("restart recovery mutated secrets: set=%d delete=%d replace=%v", secrets.setCalls, secrets.deleteCalls, secrets.replaceCalls)
	}
}

func TestLegacyProfileReplaceFailurePreservesProfileAndSecretEvidence(t *testing.T) {
	original := Profile{ID: "profile-a", Name: "Original", Kind: "postgres", Host: "db.example", User: "reader"}
	original.SecretRef = legacyDBCredentialRef(original.ID)
	wantErr := errors.New("injected atomic replacement failure")
	secrets := &fakeSecretStore{
		values:     map[string]string{original.SecretRef: "legacy password"},
		replaceErr: wantErr,
	}
	svc := &Service{
		path:     filepath.Join(t.TempDir(), "profiles.json"),
		profiles: []Profile{original},
		secrets:  secrets,
	}
	update := original
	update.Password = "explicit replacement"

	if _, err := svc.SaveProfile(update); !errors.Is(err, wantErr) {
		t.Fatalf("SaveProfile error = %v, want replacement error", err)
	}
	if len(svc.profiles) != 1 || svc.profiles[0] != original {
		t.Fatalf("replacement failure changed profile memory: %+v", svc.profiles)
	}
	if got := secrets.values[original.SecretRef]; got != "legacy password" {
		t.Fatalf("replacement failure changed legacy evidence to %q", got)
	}
	if _, err := os.Stat(svc.path); !os.IsNotExist(err) {
		t.Fatalf("replacement failure unexpectedly wrote profiles: %v", err)
	}
}

func TestLegacyProfileReentryRejectsSimultaneousScopeChangeForRecoverability(t *testing.T) {
	original := Profile{ID: "profile-a", Name: "Original", Kind: "postgres", Host: "old.example", User: "reader"}
	original.SecretRef = legacyDBCredentialRef(original.ID)
	secrets := &fakeSecretStore{values: map[string]string{original.SecretRef: "legacy password"}}
	svc := &Service{
		path:     filepath.Join(t.TempDir(), "profiles.json"),
		profiles: []Profile{original},
		secrets:  secrets,
	}
	update := original
	update.Host = "new.example"
	update.Password = "explicit replacement"

	if _, err := svc.SaveProfile(update); err == nil || !strings.Contains(err.Error(), "save destination changes separately") {
		t.Fatalf("SaveProfile error = %v, want recoverability guard", err)
	}
	if len(svc.profiles) != 1 || svc.profiles[0] != original {
		t.Fatalf("rejected scope change modified profile: %+v", svc.profiles)
	}
	if got := secrets.values[original.SecretRef]; got != "legacy password" {
		t.Fatalf("rejected scope change modified legacy evidence to %q", got)
	}
	if secrets.setCalls != 0 || secrets.deleteCalls != 0 || len(secrets.replaceCalls) != 0 {
		t.Fatalf("rejected scope change mutated secrets: set=%d delete=%d replace=%v", secrets.setCalls, secrets.deleteCalls, secrets.replaceCalls)
	}
}

func TestOpenNeverResolvesForeignSecretRef(t *testing.T) {
	secrets := &fakeSecretStore{values: map[string]string{"llm.apikey": "llm-key"}}
	svc := &Service{secrets: secrets}
	conn, err := svc.open(Profile{
		ID:        "profile-a",
		Name:      "A",
		Kind:      "postgres",
		Host:      "db.example",
		User:      "reader",
		SecretRef: "llm.apikey",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if len(secrets.getRefs) != 0 {
		t.Fatalf("foreign secret refs were resolved at point of use: %v", secrets.getRefs)
	}
}

func TestOpenFailsClosedForUnavailableOwnedCredential(t *testing.T) {
	profile := Profile{ID: "profile-a", Name: "A", Kind: "postgres", Host: "db.example", User: "reader"}
	profile.SecretRef = dbCredentialRef(profile)

	t.Run("missing fallback credential", func(t *testing.T) {
		svc := &Service{secrets: &fakeSecretStore{values: map[string]string{}}}
		if conn, err := svc.open(profile); err == nil {
			_ = conn.Close()
			t.Fatal("open accepted a missing owned credential")
		}
	})

	t.Run("empty fallback credential", func(t *testing.T) {
		svc := &Service{secrets: &fakeSecretStore{values: map[string]string{profile.SecretRef: ""}}}
		if conn, err := svc.open(profile); err == nil {
			_ = conn.Close()
			t.Fatal("open accepted an empty owned credential")
		}
	})

	t.Run("unhealthy credential storage", func(t *testing.T) {
		wantErr := errors.New("master key unavailable")
		svc := &Service{secrets: &fakeSecretStore{
			values:    map[string]string{profile.SecretRef: "secret"},
			healthErr: wantErr,
		}}
		if conn, err := svc.open(profile); !errors.Is(err, wantErr) {
			if conn != nil {
				_ = conn.Close()
			}
			t.Fatalf("open error = %v, want wrapped health error", err)
		}
	})

	t.Run("checked decrypt failure", func(t *testing.T) {
		wantErr := errors.New("authentication tag mismatch")
		secrets := &checkedFakeSecretStore{
			fakeSecretStore: &fakeSecretStore{values: map[string]string{profile.SecretRef: "opaque"}},
			checkedErr:      wantErr,
		}
		svc := &Service{secrets: secrets}
		if conn, err := svc.open(profile); !errors.Is(err, wantErr) {
			if conn != nil {
				_ = conn.Close()
			}
			t.Fatalf("open error = %v, want wrapped checked-read error", err)
		}
		if len(secrets.getRefs) != 1 || secrets.getRefs[0] != profile.SecretRef {
			t.Fatalf("GetChecked refs = %v, want only %q", secrets.getRefs, profile.SecretRef)
		}
	})

	t.Run("legacy owned credential requires migration", func(t *testing.T) {
		legacy := profile
		legacy.SecretRef = legacyDBCredentialRef(legacy.ID)
		svc := &Service{secrets: &fakeSecretStore{values: map[string]string{legacy.SecretRef: "secret"}}}
		if conn, err := svc.open(legacy); err == nil {
			_ = conn.Close()
			t.Fatal("open silently connected without resolving a legacy owned credential")
		}
	})
}

func TestProfilePersistenceFailureRollsBackMemoryAndSecret(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := Profile{ID: "profile-a", Name: "Original", Kind: "postgres", Host: "db.example", User: "reader"}
	original.SecretRef = dbCredentialRef(original)
	secrets := &fakeSecretStore{values: map[string]string{original.SecretRef: "old-password"}}
	svc := &Service{
		path:     filepath.Join(blocker, "profiles.json"),
		profiles: []Profile{original},
		secrets:  secrets,
	}
	update := original
	update.Name = "Failed update"
	update.Password = "new-password"
	if _, err := svc.SaveProfile(update); err == nil {
		t.Fatal("SaveProfile unexpectedly succeeded with an unwritable profile path")
	}
	if len(svc.profiles) != 1 || svc.profiles[0].Name != original.Name {
		t.Fatalf("failed save leaked into memory: %+v", svc.profiles)
	}
	if got := secrets.values[original.SecretRef]; got != "old-password" {
		t.Fatalf("failed save left replacement credential %q", got)
	}

	if err := svc.DeleteProfile(original.ID); err == nil {
		t.Fatal("DeleteProfile unexpectedly succeeded with an unwritable profile path")
	}
	if len(svc.profiles) != 1 || svc.profiles[0].ID != original.ID {
		t.Fatalf("failed delete leaked into memory: %+v", svc.profiles)
	}
	if got := secrets.values[original.SecretRef]; got != "old-password" {
		t.Fatalf("failed delete changed credential to %q", got)
	}
}

func TestUnhealthySecretStorePreservesOwnedDBReferenceAndBlocksCredentialMutation(t *testing.T) {
	original := Profile{ID: "profile-a", Name: "A", Kind: "postgres", Host: "db.example", User: "reader"}
	original.SecretRef = dbCredentialRef(original)
	raw, err := json.MarshalIndent([]Profile{original}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("master key corrupt")
	secrets := &fakeSecretStore{
		values:    map[string]string{original.SecretRef: "ciphertext-placeholder"},
		healthErr: wantErr,
	}
	svc := &Service{path: path, profiles: []Profile{}, secrets: secrets}
	svc.load()
	if len(svc.profiles) != 1 || svc.profiles[0].SecretRef != original.SecretRef {
		t.Fatalf("load stripped owned credential metadata while store was unhealthy: %+v", svc.profiles)
	}

	replacement := original
	replacement.Password = "new-password"
	if _, err := svc.SaveProfile(replacement); !errors.Is(err, wantErr) {
		t.Fatalf("SaveProfile error = %v, want health error", err)
	}
	if err := svc.DeleteProfile(original.ID); !errors.Is(err, wantErr) {
		t.Fatalf("DeleteProfile error = %v, want health error", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("profile or purpose-bound credential ownership changed while secret store was unhealthy")
	}
	if secrets.values[original.SecretRef] != "ciphertext-placeholder" {
		t.Fatal("credential evidence changed while secret store was unhealthy")
	}
}

func TestCredentialStatusesRequireReadableOwnedCredential(t *testing.T) {
	verified := Profile{ID: "verified", Name: "Verified", Kind: "postgres", Host: "verified.example", User: "reader"}
	verified.SecretRef = dbCredentialRef(verified)
	quarantined := Profile{ID: "legacy", Name: "Legacy", Kind: "mysql", Host: "legacy.example", User: "reader"}
	quarantined.SecretRef = legacyDBCredentialRef(quarantined.ID)
	missing := Profile{ID: "missing", Name: "Missing", Kind: "postgres", Host: "missing.example", User: "reader"}
	missing.SecretRef = dbCredentialRef(missing)
	foreign := Profile{ID: "foreign", Name: "Foreign", Kind: "postgres", Host: "foreign.example", User: "reader", SecretRef: foreignLLMRefForTest()}
	none := Profile{ID: "sqlite", Name: "SQLite", Kind: "sqlite", File: "test.db"}
	secrets := &checkedManyFakeSecretStore{
		fakeSecretStore: &fakeSecretStore{values: map[string]string{}},
		checkedValues: map[string]string{
			verified.SecretRef:    "verified-password",
			quarantined.SecretRef: "legacy-password",
		},
	}
	svc := &Service{profiles: []Profile{verified, quarantined, missing, foreign, none}, secrets: secrets}

	got := map[string]string{}
	for _, status := range svc.CredentialStatuses() {
		got[status.ProfileID] = status.Status
	}
	want := map[string]string{
		"verified": "verified", "legacy": "quarantined", "missing": "unavailable", "foreign": "unavailable", "sqlite": "none",
	}
	if !mapsEqual(got, want) {
		t.Fatalf("credential statuses = %v, want %v", got, want)
	}
	if len(secrets.checkedRefs) != 1 {
		t.Fatalf("checked snapshots = %d, want one", len(secrets.checkedRefs))
	}
	for _, ref := range secrets.checkedRefs[0] {
		if ref == foreign.SecretRef {
			t.Fatalf("status inspection resolved caller-owned foreign ref %q", ref)
		}
	}

	secrets.checkedErr = errors.New("encrypted store unavailable")
	got = map[string]string{}
	for _, status := range svc.CredentialStatuses() {
		got[status.ProfileID] = status.Status
	}
	if got[verified.ID] != "unavailable" || got[quarantined.ID] != "unavailable" || got[none.ID] != "none" {
		t.Fatalf("checked-read failure did not fail closed: %v", got)
	}
}

func TestSaveProfileReconciledReturnsPublishedIdentityAndLatchesWarning(t *testing.T) {
	cause := errors.New("directory sync failed")
	writes := 0
	svc := &Service{
		path: filepath.Join(t.TempDir(), "profiles.json"),
		writeAtomic: func(path string, data []byte, perm os.FileMode) error {
			writes++
			return &persistfile.PublishedError{Path: path, Err: cause}
		},
	}

	outcome, err := svc.SaveProfileReconciled(Profile{Name: "Published", Kind: "sqlite", File: "published.db"})
	if err != nil {
		t.Fatalf("SaveProfileReconciled error = %v", err)
	}
	if outcome.Profile.ID == "" || outcome.Profile.Name != "Published" {
		t.Fatalf("published identity was lost: %+v", outcome)
	}
	if outcome.FinalizationWarning == "" || !strings.Contains(outcome.FinalizationWarning, "could not be safely finalized") {
		t.Fatalf("finalization warning = %q", outcome.FinalizationWarning)
	}
	profiles := svc.ListProfiles()
	if len(profiles) != 1 || profiles[0].ID != outcome.Profile.ID {
		t.Fatalf("visible forward-committed state = %+v, want id %q", profiles, outcome.Profile.ID)
	}
	if _, err := svc.SaveProfileReconciled(Profile{Name: "Duplicate retry", Kind: "sqlite", File: "duplicate.db"}); err == nil {
		t.Fatal("mutation was not blocked after uncertain finalization")
	}
	if writes != 1 {
		t.Fatalf("persistence attempts = %d, want one", writes)
	}
}

func TestReconciledMutationsDoNotMisclassifySecretStorePublicationErrors(t *testing.T) {
	cause := errors.New("secret directory sync failed")
	secretPublished := &persistfile.PublishedError{Path: "secrets.json", Err: cause}

	t.Run("save", func(t *testing.T) {
		secrets := &fakeSecretStore{values: map[string]string{}, setErr: secretPublished}
		svc := &Service{path: filepath.Join(t.TempDir(), "profiles.json"), secrets: secrets}
		outcome, err := svc.SaveProfileReconciled(Profile{
			Name: "A", Kind: "postgres", Host: "db.example", User: "reader", Password: "password",
		})
		if !errors.Is(err, cause) || outcome.Profile.ID != "" || outcome.FinalizationWarning != "" {
			t.Fatalf("secret publication was misclassified as a profile commit: outcome=%+v err=%v", outcome, err)
		}
		if len(svc.ListProfiles()) != 0 || svc.LoadError() != "" {
			t.Fatalf("secret error changed/latching profile state: profiles=%+v loadErr=%q", svc.ListProfiles(), svc.LoadError())
		}
	})

	t.Run("delete health gate", func(t *testing.T) {
		profile := Profile{ID: "profile-a", Name: "A", Kind: "postgres", Host: "db.example", User: "reader"}
		profile.SecretRef = dbCredentialRef(profile)
		secrets := &fakeSecretStore{values: map[string]string{profile.SecretRef: "password"}, healthErr: secretPublished}
		svc := &Service{path: filepath.Join(t.TempDir(), "profiles.json"), profiles: []Profile{profile}, secrets: secrets}
		outcome, err := svc.DeleteProfileReconciled(profile.ID)
		if !errors.Is(err, cause) || outcome.Deleted || outcome.FinalizationWarning != "" {
			t.Fatalf("secret health error was misclassified as a profile deletion: outcome=%+v err=%v", outcome, err)
		}
		if profiles := svc.ListProfiles(); len(profiles) != 1 || profiles[0].ID != profile.ID || svc.LoadError() != "" {
			t.Fatalf("secret health error changed/latching profile state: profiles=%+v loadErr=%q", profiles, svc.LoadError())
		}
	})
}

func TestDeleteProfileReconciledKeepsPublishedDeletionAndRecoverableSecret(t *testing.T) {
	profile := Profile{ID: "profile-a", Name: "A", Kind: "postgres", Host: "db.example", User: "reader"}
	profile.SecretRef = dbCredentialRef(profile)
	secrets := &fakeSecretStore{values: map[string]string{profile.SecretRef: "password"}}
	cause := errors.New("directory sync failed")
	svc := &Service{
		path:     filepath.Join(t.TempDir(), "profiles.json"),
		profiles: []Profile{profile},
		secrets:  secrets,
		writeAtomic: func(path string, data []byte, perm os.FileMode) error {
			return &persistfile.PublishedError{Path: path, Err: cause}
		},
	}

	outcome, err := svc.DeleteProfileReconciled(profile.ID)
	if err != nil {
		t.Fatalf("DeleteProfileReconciled error = %v", err)
	}
	if !outcome.Deleted || outcome.FinalizationWarning == "" {
		t.Fatalf("published delete outcome = %+v", outcome)
	}
	if profiles := svc.ListProfiles(); len(profiles) != 0 {
		t.Fatalf("published deletion was rolled back in memory: %+v", profiles)
	}
	if got := secrets.values[profile.SecretRef]; got != "password" || secrets.deleteCalls != 0 {
		t.Fatalf("recoverable credential evidence changed: value=%q deletes=%d", got, secrets.deleteCalls)
	}
	if err := svc.DeleteProfile(profile.ID); err == nil || !strings.Contains(err.Error(), "could not be safely finalized") {
		t.Fatalf("later mutation error = %v, want latched finalization warning", err)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func seedSQLite(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO users (id, name) VALUES (1, 'Ada'), (2, 'Bob');`); err != nil {
		t.Fatal(err)
	}
	return path
}
