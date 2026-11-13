package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore is an in-memory SecretStore so the wiring tests run without a real
// keychain or a platform dependency.
type fakeStore struct {
	data    map[string]string
	loadErr error
	name    string
}

func newFakeStore() *fakeStore { return &fakeStore{data: map[string]string{}} }

func (f *fakeStore) Load(account string) (string, bool, error) {
	if f.loadErr != nil {
		return "", false, f.loadErr
	}
	v, ok := f.data[account]
	return v, ok, nil
}
func (f *fakeStore) Save(account, secret string) error { f.data[account] = secret; return nil }
func (f *fakeStore) Delete(account string) error       { delete(f.data, account); return nil }
func (f *fakeStore) Name() string {
	if f.name != "" {
		return f.name
	}
	return "Fake Store"
}

// useStore points currentSecretStore at s for the test, and restores it after.
// A nil s models the file fallback and keeps the real reason, which under
// `go test` is that secure stores are off.
func useStore(t *testing.T, s SecretStore) {
	t.Helper()
	orig := currentSecretStore
	currentSecretStore = func() (SecretStore, string) {
		if s == nil {
			return chooseSecretStore(currentSecretStoreEnv())
		}
		return s, ""
	}
	t.Cleanup(func() { currentSecretStore = orig })
}

// credStoreFixtureWithAuth is credStoreFixture with an auth handler registered,
// for tests that read auth status. UseAuth initializes Creds itself.
func credStoreFixtureWithAuth(t *testing.T) {
	t.Helper()
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", stubAuthHandler{})
	viper.Set("profile", "acme")
}

// credStoreFixture gives a CLI with Creds initialized and the "acme" profile
// active, ready for SetProfile/GetProfile.
func credStoreFixture(t *testing.T) {
	t.Helper()
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	InitCredentialsFile()
	viper.Set("profile", "acme")
}

func TestSetProfileMovesAPIKeyToTheStore(t *testing.T) {
	credStoreFixture(t)
	store := newFakeStore()
	useStore(t, store)

	Creds.SetProfile("acme", map[string]string{"type": "apikey", apiKeyField: "sk-orq-secret"})
	require.NoError(t, Creds.Save(credentialsFile()))

	// The secret is in the store under the profile name, not in the file.
	assert.Equal(t, "sk-orq-secret", store.data["acme"])
	data, err := os.ReadFile(credentialsFile())
	require.NoError(t, err)
	assert.NotContains(t, string(data), "sk-orq-secret", "api key must not be written to credentials.json")
	assert.Contains(t, string(data), apiKeyStoreField, "a marker must be left in credentials.json")

	// GetProfile splices the key back from the store.
	assert.Equal(t, "sk-orq-secret", GetProfile()[apiKeyField])
	assert.Equal(t, "apikey", GetProfile()["type"])
}

func TestGetProfileMigratesLegacyPlaintextKeyOnRead(t *testing.T) {
	credStoreFixture(t)
	store := newFakeStore()
	useStore(t, store)

	// Seed a legacy profile whose key sits in the file, as if written before the
	// store existed, bypassing the SetProfile interception.
	profiles := map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyField: "sk-orq-legacy"},
	}
	Creds.Set("profiles", profiles)
	require.NoError(t, Creds.Save(credentialsFile()))

	// First read returns the key and migrates it into the store.
	assert.Equal(t, "sk-orq-legacy", GetProfile()[apiKeyField])
	assert.Equal(t, "sk-orq-legacy", store.data["acme"])

	// The file no longer holds the plaintext, just the marker.
	data, err := os.ReadFile(credentialsFile())
	require.NoError(t, err)
	assert.NotContains(t, string(data), "sk-orq-legacy")
	assert.Contains(t, string(data), apiKeyStoreField)

	// A second read serves from the store (clear the store to prove the file has
	// no plaintext fallback left to read).
	store.data["acme"] = "sk-orq-rotated"
	assert.Equal(t, "sk-orq-rotated", GetProfile()[apiKeyField])
}

func TestFileFallbackKeepsTheKeyInTheFile(t *testing.T) {
	credStoreFixture(t)
	useStore(t, nil) // no secure store (ORQ_CREDENTIAL_STORE=file, Windows, headless)

	Creds.SetProfile("acme", map[string]string{"type": "apikey", apiKeyField: "sk-orq-plain"})
	require.NoError(t, Creds.Save(credentialsFile()))

	data, err := os.ReadFile(credentialsFile())
	require.NoError(t, err)
	assert.Contains(t, string(data), "sk-orq-plain", "with no store the key stays in the 0600 file")
	assert.Equal(t, "sk-orq-plain", GetProfile()[apiKeyField])
}

func TestMarkedProfileWithUnreadableStoreReturnsNoKey(t *testing.T) {
	credStoreFixture(t)
	store := newFakeStore()
	store.loadErr = fmt.Errorf("keychain is locked")
	useStore(t, store)

	// A profile that has already been migrated (marker, no plaintext).
	Creds.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyStoreField: "Fake Store"},
	})

	// A store that cannot answer must yield no key, never a wrong one, and never panic.
	_, present := GetProfile()[apiKeyField]
	assert.False(t, present, "an unreadable store must not surface a key")
}

func TestStoreSaveFailureKeepsKeyInFile(t *testing.T) {
	credStoreFixture(t)
	useStore(t, failingSaveStore{})

	Creds.SetProfile("acme", map[string]string{"type": "apikey", apiKeyField: "sk-orq-kept"})
	require.NoError(t, Creds.Save(credentialsFile()))

	// Moving the key must never lose it: a failed store write leaves it in the file.
	data, err := os.ReadFile(credentialsFile())
	require.NoError(t, err)
	assert.Contains(t, string(data), "sk-orq-kept")
}

type failingSaveStore struct{}

func (failingSaveStore) Load(string) (string, bool, error) { return "", false, nil }
func (failingSaveStore) Save(string, string) error         { return fmt.Errorf("store write failed") }
func (failingSaveStore) Delete(string) error               { return nil }
func (failingSaveStore) Name() string                      { return "Failing Store" }

func TestRemoveProfileClearsTheStoredKey(t *testing.T) {
	credStoreFixture(t)
	store := newFakeStore()
	useStore(t, store)

	Creds.SetProfile("acme", map[string]string{"type": "apikey", apiKeyField: "sk-orq-secret"})
	if _, ok := store.data["acme"]; !ok {
		t.Fatal("precondition: the key should be in the store after SetProfile")
	}

	Creds.RemoveProfile("acme")
	if _, ok := store.data["acme"]; ok {
		t.Error("RemoveProfile left the API key orphaned in the secure store")
	}
}

// ---- macOS security(1) store, exercised through a fake runSecretTool ----

type fakeExit struct{ code int }

func (f fakeExit) Error() string { return fmt.Sprintf("exit status %d", f.code) }
func (f fakeExit) ExitCode() int { return f.code }

func argVal(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// fakeKeychain stands in for `security`, keyed by service+account, so the ported
// chunking Load/Save/Delete can be exercised deterministically.
func fakeKeychain(t *testing.T) map[string]string {
	t.Helper()
	kc := map[string]string{}
	orig := runSecretTool
	runSecretTool = func(stdin, name string, args ...string) (string, string, error) {
		if name != "security" {
			return "", "", fmt.Errorf("unexpected tool %q", name)
		}
		switch args[0] {
		case "find-generic-password":
			k := argVal(args, "-s") + "\x00" + argVal(args, "-a")
			if v, ok := kc[k]; ok {
				return v + "\n", "", nil
			}
			return "", "", fakeExit{44}
		case "delete-generic-password":
			k := argVal(args, "-s") + "\x00" + argVal(args, "-a")
			if _, ok := kc[k]; ok {
				delete(kc, k)
				return "", "", nil
			}
			return "", "", fakeExit{44}
		case "-i": // stdin: add-generic-password -U -s S -a A -w V
			f := splitKeychainLine(stdin)
			// The real parser rejects a stray argument with exit 2 and stores nothing.
			if len(f) != 8 {
				return "", "", fakeExit{2}
			}
			k := argVal(f, "-s") + "\x00" + argVal(f, "-a")
			kc[k] = argVal(f, "-w")
			return "", "", nil
		}
		return "", "", fmt.Errorf("unexpected security subcommand %q", args[0])
	}
	t.Cleanup(func() { runSecretTool = orig })
	return kc
}

// splitKeychainLine splits a `security -i` line the way the real parser does:
// on whitespace, except inside double quotes, where a backslash escapes.
func splitKeychainLine(line string) []string {
	var fields []string
	var current strings.Builder
	inQuotes, started := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inQuotes && c == '\\' && i+1 < len(line):
			i++
			current.WriteByte(line[i])
		case c == '"':
			inQuotes, started = !inQuotes, true
		case !inQuotes && (c == ' ' || c == '\n'):
			if started {
				fields = append(fields, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteByte(c)
			started = true
		}
	}
	return fields
}

func TestMacKeychainStoreRoundTrips(t *testing.T) {
	resetAuthState(t)
	viper.Set("app-name", "test-auth")
	fakeKeychain(t)
	s := macKeychainStore{}

	// Not found is a clean miss, not an error.
	_, ok, err := s.Load("acme")
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, s.Save("acme", "sk-orq-small"))
	got, ok, err := s.Load("acme")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "sk-orq-small", got)

	require.NoError(t, s.Delete("acme"))
	_, ok, _ = s.Load("acme")
	assert.False(t, ok)
}

func TestMacKeychainStoreChunksALargeSecret(t *testing.T) {
	resetAuthState(t)
	viper.Set("app-name", "test-auth")
	kc := fakeKeychain(t)
	s := macKeychainStore{}

	// Larger than one command line, to force continuation chunks.
	big := strings.Repeat("A", maxKeychainCommand*3)
	require.NoError(t, s.Save("acme", big))
	assert.Greater(t, len(kc), 1, "a large secret must span more than one keychain item")

	got, ok, err := s.Load("acme")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, big, got, "the chunks must rejoin to the exact secret")

	// Shrinking back to a one-item secret must clear the continuation chunks.
	require.NoError(t, s.Save("acme", "tiny"))
	got, _, err = s.Load("acme")
	require.NoError(t, err)
	assert.Equal(t, "tiny", got)
	assert.Equal(t, 1, len(kc), "leftover continuation chunks must be cleared")
}

// credentialsOnDisk reads the file as another process would see it.
func credentialsOnDisk(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(credentialsFile())
	require.NoError(t, err)
	return string(data)
}

func TestNewPlaintextKeyClearsAStaleMarker(t *testing.T) {
	for name, next := range map[string]SecretStore{"no store": nil, "failed save": failingSaveStore{}} {
		t.Run(name, func(t *testing.T) {
			credStoreFixture(t)
			useStore(t, newFakeStore())
			Creds.SetProfile("acme", map[string]string{"type": "apikey", apiKeyField: "secret-one"})

			useStore(t, next)
			Creds.SetProfile("acme", map[string]string{apiKeyField: "secret-two"})
			require.NoError(t, Creds.Save(credentialsFile()))

			data := credentialsOnDisk(t)
			assert.Contains(t, data, "secret-two", "the key must stay in the file")
			assert.NotContains(t, data, apiKeyStoreField, "the file must not claim the key moved")
		})
	}
}

func TestMarkedProfileHoldingAPlaintextKeyMigrates(t *testing.T) {
	credStoreFixture(t)
	store := newFakeStore()
	store.data["acme"] = "secret-old"
	useStore(t, store)
	// The shape a stale marker left in files written before this was fixed.
	Creds.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyField: "secret-new", apiKeyStoreField: "Fake Store"},
	})
	require.NoError(t, Creds.Save(credentialsFile()))

	assert.Equal(t, "secret-new", GetProfile()[apiKeyField])
	assert.Equal(t, "secret-new", store.data["acme"])
	assert.NotContains(t, credentialsOnDisk(t), "secret-new")
}

func TestFailedMigrationKeepsThePlaintextOnDisk(t *testing.T) {
	credStoreFixture(t)
	useStore(t, failingSaveStore{})
	Creds.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyField: "sk-orq-legacy"},
	})
	require.NoError(t, Creds.Save(credentialsFile()))

	assert.Equal(t, "sk-orq-legacy", GetProfile()[apiKeyField])
	data := credentialsOnDisk(t)
	assert.Contains(t, data, "sk-orq-legacy", "a failed move must not lose the key")
	assert.NotContains(t, data, apiKeyStoreField)
}

func TestMigrationKeepsAProfileAnotherProcessWrote(t *testing.T) {
	credStoreFixture(t)
	store := newFakeStore()
	useStore(t, store)
	Creds.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyField: "sk-orq-legacy"},
	})
	require.NoError(t, Creds.Save(credentialsFile()))

	other, err := NewCredentialsFile(filepath.Dir(credentialsFile()))
	require.NoError(t, err)
	other.Set("profiles", map[string]interface{}{
		"acme":  map[string]interface{}{"type": "apikey", apiKeyField: "sk-orq-legacy"},
		"later": map[string]interface{}{"type": "apikey", "server": "https://later.example"},
	})
	require.NoError(t, other.Save(credentialsFile()))

	GetProfile()

	data := credentialsOnDisk(t)
	assert.Contains(t, data, "later.example", "a read must not drop what another process wrote")
	assert.NotContains(t, data, "sk-orq-legacy")
	assert.Contains(t, data, apiKeyStoreField)
}

func TestMigrationLeavesAKeyChangedOnDiskAlone(t *testing.T) {
	credStoreFixture(t)
	useStore(t, newFakeStore())
	Creds.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyField: "sk-orq-legacy"},
	})
	require.NoError(t, Creds.Save(credentialsFile()))

	other, err := NewCredentialsFile(filepath.Dir(credentialsFile()))
	require.NoError(t, err)
	other.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyField: "sk-orq-rotated"},
	})
	require.NoError(t, other.Save(credentialsFile()))

	GetProfile()

	assert.Contains(t, credentialsOnDisk(t), "sk-orq-rotated", "the store holds the old key, so the new one must stay")
}

func TestUnreadableStoreWarnsAndNamesTheStore(t *testing.T) {
	cases := map[string]struct {
		store SecretStore
		want  []string
	}{
		"locked": {func() SecretStore {
			s := newFakeStore()
			s.loadErr = fmt.Errorf("keychain is locked")
			return s
		}(), []string{"Fake Store", "keychain is locked"}},
		"entry gone": {newFakeStore(), []string{"Fake Store", "no entry"}},
		"no store":   {nil, []string{"Fake Store", "secure stores are off under go test"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			credStoreFixture(t)
			useStore(t, tc.store)
			var logged bytes.Buffer
			previous := zlog.Logger
			zlog.Logger = zerolog.New(&logged).Level(zerolog.WarnLevel)
			t.Cleanup(func() { zlog.Logger = previous })
			Creds.Set("profiles", map[string]interface{}{
				"acme": map[string]interface{}{"type": "apikey", apiKeyStoreField: "Fake Store"},
			})

			_, present := GetProfile()[apiKeyField]

			assert.False(t, present)
			for _, want := range tc.want {
				assert.Contains(t, logged.String(), want)
			}
		})
	}
}

func TestStoreMarkerIsNotTreatedAsASecret(t *testing.T) {
	assert.False(t, looksSensitiveKey(apiKeyStoreField))
	assert.True(t, looksSensitiveKey(apiKeyField))
}

func TestResolveSecretStoreStaysOffTheKeychainUnderGoTest(t *testing.T) {
	assert.Nil(t, resolveSecretStore())
	store, reason := currentSecretStore()
	assert.Nil(t, store)
	assert.Equal(t, "secure stores are off under go test", reason)
}

func TestMacKeychainStoreHandlesAProfileNameWithASpace(t *testing.T) {
	kc := fakeKeychain(t)
	store := macKeychainStore{}

	require.NoError(t, store.Save("my work", "sk-orq-secret"))
	assert.Contains(t, kc, secretService()+"\x00my work")

	got, ok, err := store.Load("my work")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "sk-orq-secret", got)
	assert.Equal(t, `add-generic-password -U -s "svc" -a "a\"b\\c" -w v`+"\n", addGenericPasswordLine("svc", `a"b\c`, "v"))
}

func TestMacKeychainStoreRefusesALineBreakInAName(t *testing.T) {
	kc := fakeKeychain(t)
	assert.Error(t, macKeychainStore{}.Save("acme\nwork", "sk-orq-secret"))
	assert.Empty(t, kc)
}

func TestStoresReadAnEmptySecretAsMissing(t *testing.T) {
	kc := fakeKeychain(t)
	kc[secretService()+"\x00acme"] = ""
	_, ok, err := macKeychainStore{}.Load("acme")
	require.NoError(t, err)
	assert.False(t, ok, "macOS Keychain")

	orig := runSecretTool
	runSecretTool = func(string, string, ...string) (string, string, error) { return "", "", nil }
	t.Cleanup(func() { runSecretTool = orig })
	_, ok, err = secretToolStore{}.Load("acme")
	require.NoError(t, err)
	assert.False(t, ok, "secret-tool")
}

func TestManualClearHintQuotesTheNames(t *testing.T) {
	assert.Contains(t, manualClearHint(macKeychainStore{}, "my work"), `-a "my work"`)
	assert.Contains(t, manualClearHint(secretToolStore{}, "my work"), `account "my work"`)
}

// countingStore counts store calls, which on macOS each start a process.
type countingStore struct {
	*fakeStore
	loads, saves int
	saveErr      error
}

func (c *countingStore) Load(account string) (string, bool, error) {
	c.loads++
	return c.fakeStore.Load(account)
}

func (c *countingStore) Save(account, secret string) error {
	c.saves++
	if c.saveErr != nil {
		return c.saveErr
	}
	return c.fakeStore.Save(account, secret)
}

// This process still holds K1 in memory. Another process rotated to K2 and moved
// it to the store, so saving K1 would leave the profile on the old key for good.
func TestMigrationDoesNotOverwriteANewerStoredKey(t *testing.T) {
	credStoreFixture(t)
	store := newFakeStore()
	useStore(t, store)
	Creds.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyField: "sk-orq-k1"},
	})

	other, err := NewCredentialsFile(filepath.Dir(credentialsFile()))
	require.NoError(t, err)
	other.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyStoreField: store.Name()},
	})
	require.NoError(t, other.Save(credentialsFile()))
	require.NoError(t, store.Save("acme", "sk-orq-k2"))

	GetProfile()

	assert.Equal(t, "sk-orq-k2", store.data["acme"])
}

func TestGetProfileReadsTheStoreOncePerProcess(t *testing.T) {
	credStoreFixture(t)
	store := &countingStore{fakeStore: newFakeStore()}
	useStore(t, store)
	Creds.SetProfile("acme", map[string]string{"type": "apikey", apiKeyField: "sk-orq-secret"})

	for range 3 {
		assert.Equal(t, "sk-orq-secret", GetProfile()[apiKeyField])
	}
	assert.Equal(t, 1, store.loads)
}

func TestRotatingAKeyHereIsSeenWithoutARestart(t *testing.T) {
	credStoreFixture(t)
	useStore(t, newFakeStore())
	Creds.SetProfile("acme", map[string]string{"type": "apikey", apiKeyField: "sk-orq-old"})
	require.Equal(t, "sk-orq-old", GetProfile()[apiKeyField])

	Creds.SetProfile("acme", map[string]string{apiKeyField: "sk-orq-new"})

	assert.Equal(t, "sk-orq-new", GetProfile()[apiKeyField])
}

func TestAFailedStoreReadRetriesAndWarnsOnce(t *testing.T) {
	credStoreFixture(t)
	store := &countingStore{fakeStore: newFakeStore()}
	useStore(t, store)
	Creds.SetProfile("acme", map[string]string{"type": "apikey", apiKeyField: "sk-orq-secret"})
	store.loadErr = errors.New("keychain locked")

	var logs bytes.Buffer
	orig := zlog.Logger
	zlog.Logger = zerolog.New(&logs)
	t.Cleanup(func() { zlog.Logger = orig })

	GetProfile()
	GetProfile()
	assert.Equal(t, 1, strings.Count(logs.String(), "could not read the API key"))

	store.loadErr = nil
	assert.Equal(t, "sk-orq-secret", GetProfile()[apiKeyField], "unlocking the store must work without a restart")
}

func TestAFailedMigrationIsTriedOncePerProcess(t *testing.T) {
	credStoreFixture(t)
	store := &countingStore{fakeStore: newFakeStore(), saveErr: errors.New("keychain locked")}
	useStore(t, store)
	Creds.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyField: "sk-orq-legacy"},
	})
	require.NoError(t, Creds.Save(credentialsFile()))

	GetProfile()
	GetProfile()

	assert.Equal(t, 1, store.saves)
}

// A store that cannot be read must not look like a missing key: the key still
// exists, and the remedy `auth setup` would replace it. The status says so and
// `doctor --fix` leaves it alone.
func TestAStoreThatCannotBeReadIsNotReportedAsMissing(t *testing.T) {
	cases := map[string]struct {
		store SecretStore
		want  []string
	}{
		"locked": {func() SecretStore {
			s := newFakeStore()
			s.loadErr = errors.New("keychain is locked")
			return s
		}(), []string{"Fake Store", "keychain is locked"}},
		"no store here": {nil, []string{"Fake Store", "secure stores are off under go test"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			credStoreFixtureWithAuth(t)
			useStore(t, tc.store)
			Creds.Set("profiles", map[string]interface{}{
				"acme": map[string]interface{}{"type": "", apiKeyStoreField: "Fake Store"},
			})

			status := GetAuthStatus()

			assert.Equal(t, false, status["configured"])
			assert.Equal(t, ProfileStoreUnreadable, status["source"])
			for _, want := range tc.want {
				assert.Contains(t, status["store_error"], want)
			}
			assert.False(t, authFixableBySetup(status), "doctor --fix must not run auth setup over a stored key")
			assert.NotContains(t, doctorStatus()["checks"].(map[string]interface{})["fixable"], "auth")
		})
	}
}

// An entry that is gone from the store is a lost key, so it stays "missing" and
// `auth setup` is the right fix.
func TestAMissingStoreEntryIsStillMissing(t *testing.T) {
	credStoreFixtureWithAuth(t)
	useStore(t, newFakeStore())
	Creds.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "", apiKeyStoreField: "Fake Store"},
	})

	status := GetAuthStatus()

	assert.Equal(t, "missing", status["source"])
	assert.NotContains(t, status, "store_error")
	assert.Nil(t, ProfileKeyUnreadable("acme"))
	assert.True(t, authFixableBySetup(status))
}

func TestUnlockingTheStoreClearsTheUnreadableState(t *testing.T) {
	credStoreFixtureWithAuth(t)
	store := newFakeStore()
	useStore(t, store)
	Creds.SetProfile("acme", map[string]string{"type": "", apiKeyField: "sk-orq-secret"})
	store.loadErr = errors.New("keychain is locked")
	require.Equal(t, ProfileStoreUnreadable, GetAuthStatus()["source"])

	store.loadErr = nil

	status := GetAuthStatus()
	assert.Equal(t, "profile", status["source"])
	assert.NotContains(t, status, "store_error")
	assert.Nil(t, ProfileKeyUnreadable("acme"))
}

// The cache is keyed by profile: each marked profile must get its own key, on the
// first read and from the cache after it.
func TestEachProfileGetsItsOwnStoredKey(t *testing.T) {
	credStoreFixture(t)
	store := &countingStore{fakeStore: newFakeStore()}
	useStore(t, store)
	Creds.SetProfile("acme", map[string]string{"type": "apikey", apiKeyField: "sk-orq-acme"})
	Creds.SetProfile("beta", map[string]string{"type": "apikey", apiKeyField: "sk-orq-beta"})
	require.Equal(t, "sk-orq-acme", store.data["acme"])
	require.Equal(t, "sk-orq-beta", store.data["beta"])

	for range 2 {
		viper.Set("profile", "acme")
		assert.Equal(t, "sk-orq-acme", GetProfile()[apiKeyField])
		viper.Set("profile", "beta")
		assert.Equal(t, "sk-orq-beta", GetProfile()[apiKeyField])
	}
	assert.Equal(t, 2, store.loads, "each profile is read from the store once, then served from the cache")
}

// This process still holds K1 in plaintext. Another process rotated to K2 and left
// it in plaintext on disk, so the store must stay empty: saving K1 there would
// make the stale key the one a later migration reads back.
func TestMigrationLeavesTheStoreEmptyWhenTheFileHoldsANewerPlaintextKey(t *testing.T) {
	credStoreFixture(t)
	store := &countingStore{fakeStore: newFakeStore()}
	useStore(t, store)
	Creds.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyField: "sk-orq-k1"},
	})

	other, err := NewCredentialsFile(filepath.Dir(credentialsFile()))
	require.NoError(t, err)
	other.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "apikey", apiKeyField: "sk-orq-k2"},
	})
	require.NoError(t, other.Save(credentialsFile()))

	GetProfile()

	assert.Equal(t, 0, store.saves)
	assert.Empty(t, store.data)
	assert.Contains(t, credentialsOnDisk(t), "sk-orq-k2")
}

func TestALostEntryClearsAStaleStoreError(t *testing.T) {
	credStoreFixtureWithAuth(t)
	store := newFakeStore()
	useStore(t, store)
	Creds.SetProfile("acme", map[string]string{"type": "", apiKeyField: "sk-orq-secret"})
	store.loadErr = errors.New("keychain is locked")
	require.Equal(t, ProfileStoreUnreadable, GetAuthStatus()["source"])

	store.loadErr = nil
	require.NoError(t, store.Delete(sanitizeProfileName("acme")))

	status := GetAuthStatus()
	assert.Nil(t, ProfileKeyUnreadable("acme"), "a lost entry is missing, not unreadable")
	assert.NotEqual(t, ProfileStoreUnreadable, status["source"])
	assert.True(t, authFixableBySetup(status), "with the key gone, `auth setup` is the remedy")
}

func TestForgetClearsTheUnreadableState(t *testing.T) {
	credStoreFixtureWithAuth(t)
	store := newFakeStore()
	useStore(t, store)
	Creds.SetProfile("acme", map[string]string{"type": "", apiKeyField: "sk-orq-secret"})
	store.loadErr = errors.New("keychain is locked")
	GetAuthStatus()
	require.Error(t, ProfileKeyUnreadable("acme"))

	Creds.hydrated.forget("acme")

	assert.Nil(t, ProfileKeyUnreadable("acme"))
}

func TestNoStoreHereNamesTheCause(t *testing.T) {
	credStoreFixtureWithAuth(t)
	orig := currentSecretStore
	currentSecretStore = func() (SecretStore, string) { return nil, "no secure store is supported on windows" }
	t.Cleanup(func() { currentSecretStore = orig })
	Creds.Set("profiles", map[string]interface{}{
		"acme": map[string]interface{}{"type": "", apiKeyStoreField: "macOS Keychain"},
	})

	GetAuthStatus()

	err := ProfileKeyUnreadable("acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no secure store is supported on windows")
	assert.NotContains(t, err.Error(), "unlock")
}

func TestDoctorFixLeavesAnUnreadableStoreAlone(t *testing.T) {
	credStoreFixtureWithAuth(t)

	fixed, err := runDoctorFixes(map[string]interface{}{
		"auth": map[string]interface{}{"configured": false, "source": ProfileStoreUnreadable},
	})

	assert.NoError(t, err)
	assert.False(t, fixed, "`doctor --fix` must not run `auth setup` over a key that is only locked")
}

// Each branch of the store choice, with its exact reason: the reason is what an
// unreadable-key error tells the user, so it must match the branch that held.
func TestChooseSecretStoreNamesTheCauseThatHeld(t *testing.T) {
	has := func(tools ...string) func(string) bool {
		return func(tool string) bool {
			for _, want := range tools {
				if want == tool {
					return true
				}
			}
			return false
		}
	}
	up := func() bool { return true }
	down := func() bool { return false }
	cases := []struct {
		name      string
		env       secretStoreEnv
		wantStore SecretStore
		reason    string
	}{
		{"under test", secretStoreEnv{goos: "darwin", underTest: true, installed: has("security"), ready: up}, nil, "secure stores are off under go test"},
		{"forced file", secretStoreEnv{goos: "darwin", forcedFile: true, installed: has("security"), ready: up}, nil, "credential-store is set to file"},
		{"mac with security", secretStoreEnv{goos: "darwin", installed: has("security"), ready: down}, macKeychainStore{}, ""},
		{"mac without security", secretStoreEnv{goos: "darwin", installed: has(), ready: up}, nil, "the macOS `security` tool is not installed"},
		{"linux ready", secretStoreEnv{goos: "linux", installed: has("secret-tool"), ready: up}, secretToolStore{}, ""},
		{"linux no tool", secretStoreEnv{goos: "linux", installed: has(), ready: up}, nil, "`secret-tool` is not installed"},
		{"linux no answer", secretStoreEnv{goos: "linux", installed: has("secret-tool"), ready: down}, nil, "`secret-tool` is installed, but its Secret Service did not answer (not running, locked, or no D-Bus session)"},
		{"windows", secretStoreEnv{goos: "windows", installed: has("security", "secret-tool"), ready: up}, nil, "no secure store is supported on windows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, reason := chooseSecretStore(tc.env)
			assert.Equal(t, tc.wantStore, store)
			assert.Equal(t, tc.reason, reason)
		})
	}
}
