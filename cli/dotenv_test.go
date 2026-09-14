package cli

import (
	"os"
	"testing"

	"github.com/spf13/viper"
)

// An application .env holds far more than the CLI's own key; only the CLI's own
// variables may reach the process environment.
func TestLoadDotEnvFilesOnlyImportsOwnVariables(t *testing.T) {
	chdirToDotEnv(t, ".env", "MYAPP_API_KEY=from-dotenv\nOPENAI_API_KEY=leaked\nCUSTOM_TOKEN=custom\n")

	for _, key := range []string{"MYAPP_API_KEY", "OPENAI_API_KEY", "CUSTOM_TOKEN"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	t.Setenv("MYAPP_DOTENV", "1")
	t.Cleanup(func() { dotEnvOrigins = map[string]string{} })

	loadDotEnvFiles("MYAPP", "CUSTOM_TOKEN")

	if got := os.Getenv("MYAPP_API_KEY"); got != "from-dotenv" {
		t.Errorf("prefixed key not imported: got %q", got)
	}
	if got := os.Getenv("CUSTOM_TOKEN"); got != "custom" {
		t.Errorf("api key env var not imported: got %q", got)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "" {
		t.Errorf("unrelated key leaked into the environment: got %q", got)
	}
	if got := DotEnvOrigin("MYAPP_API_KEY"); got != ".env" {
		t.Errorf("origin not recorded: got %q", got)
	}
	if got := DotEnvOrigin("MYAPP_SERVER"); got != "" {
		t.Errorf("origin reported for a variable that was never loaded: got %q", got)
	}
}

// Which directory you are in must not decide which credentials you send, so
// nothing is read until dotenv loading is explicitly turned on.
func TestLoadDotEnvFilesOffByDefault(t *testing.T) {
	chdirToDotEnv(t, ".env", "MYAPP_API_KEY=from-dotenv\n")

	t.Setenv("MYAPP_API_KEY", "")
	os.Unsetenv("MYAPP_API_KEY")
	t.Setenv("MYAPP_DOTENV", "")
	os.Unsetenv("MYAPP_DOTENV")
	viper.Set("dotenv", nil)
	t.Cleanup(func() { dotEnvOrigins = map[string]string{} })

	loadDotEnvFiles("MYAPP", "")

	if got := os.Getenv("MYAPP_API_KEY"); got != "" {
		t.Errorf("dotenv was read without being enabled: got %q", got)
	}
}

// A config-file key would re-enable the cwd-decides-identity problem
// everywhere, so only the environment variable turns dotenv loading on.
func TestLoadDotEnvFilesIgnoresConfigKey(t *testing.T) {
	chdirToDotEnv(t, ".env", "MYAPP_API_KEY=from-dotenv\n")

	t.Setenv("MYAPP_API_KEY", "")
	os.Unsetenv("MYAPP_API_KEY")
	t.Setenv("MYAPP_DOTENV", "")
	os.Unsetenv("MYAPP_DOTENV")
	viper.Set("dotenv", true)
	t.Cleanup(func() {
		viper.Set("dotenv", nil)
		dotEnvOrigins = map[string]string{}
	})

	loadDotEnvFiles("MYAPP", "")

	if got := os.Getenv("MYAPP_API_KEY"); got != "" {
		t.Errorf("a config key enabled dotenv loading: got %q", got)
	}
}

func chdirToDotEnv(t *testing.T, filename, contents string) {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(dir+"/"+filename, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
}
