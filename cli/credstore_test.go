package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"

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

// useStore points resolveSecretStore at s for the test (nil models the file
// fallback), and restores it after.
func useStore(t *testing.T, s SecretStore) {
	t.Helper()
	orig := resolveSecretStore
	resolveSecretStore = func() SecretStore { return s }
	t.Cleanup(func() { resolveSecretStore = orig })
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
			f := strings.Fields(stdin)
			k := argVal(f, "-s") + "\x00" + argVal(f, "-a")
			kc[k] = argVal(f, "-w")
			return "", "", nil
		}
		return "", "", fmt.Errorf("unexpected security subcommand %q", args[0])
	}
	t.Cleanup(func() { runSecretTool = orig })
	return kc
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
