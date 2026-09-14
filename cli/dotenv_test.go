package cli

import (
	"os"
	"testing"
)

// An application .env holds far more than the CLI's own variables.
func TestLoadDotEnvFilesOnlyImportsOwnVariables(t *testing.T) {
	chdirToDotEnv(t, ".env", "MYAPP_API_KEY=from-dotenv\nOPENAI_API_KEY=leaked\nCUSTOM_TOKEN=custom\n")

	clearDotEnvEnv(t, "MYAPP_API_KEY", "OPENAI_API_KEY", "CUSTOM_TOKEN")
	t.Setenv("MYAPP_DOTENV", "1")

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

// A file must never overwrite an export, including one blanked on purpose.
func TestLoadDotEnvFilesNeverOverwritesAnExport(t *testing.T) {
	chdirToDotEnv(t, ".env", "MYAPP_API_KEY=from-dotenv\nMYAPP_TOKEN=from-dotenv\n")

	clearDotEnvEnv(t)
	t.Setenv("MYAPP_DOTENV", "1")
	t.Setenv("MYAPP_API_KEY", "from-shell")
	t.Setenv("MYAPP_TOKEN", "")

	loadDotEnvFiles("MYAPP", "")

	if got := os.Getenv("MYAPP_API_KEY"); got != "from-shell" {
		t.Errorf("a file overwrote an exported variable: got %q", got)
	}
	if got := os.Getenv("MYAPP_TOKEN"); got != "" {
		t.Errorf("a file refilled a variable exported as empty: got %q", got)
	}
	if got := DotEnvOrigin("MYAPP_API_KEY"); got != "" {
		t.Errorf("an exported key was reported as dotenv-sourced: got %q", got)
	}
}

// A set variable is never overwritten, so the first file read wins.
func TestLoadDotEnvFilesFirstFileWins(t *testing.T) {
	dir := chdirToDotEnv(t, ".env", "MYAPP_API_KEY=from-env\n")
	if err := os.WriteFile(dir+"/.env.local", []byte("export MYAPP_API_KEY=\"from-local\"\nMYAPP_SERVER='https://local.example.com'\n"), 0600); err != nil {
		t.Fatal(err)
	}

	clearDotEnvEnv(t, "MYAPP_API_KEY", "MYAPP_SERVER")
	t.Setenv("MYAPP_DOTENV", "1")

	loadDotEnvFiles("MYAPP", "")

	if got := os.Getenv("MYAPP_API_KEY"); got != "from-env" {
		t.Errorf(".env.local overrode .env: got %q", got)
	}
	if got := os.Getenv("MYAPP_SERVER"); got != "https://local.example.com" {
		t.Errorf("quoted export line from .env.local not imported: got %q", got)
	}
	if got := DotEnvOrigin("MYAPP_SERVER"); got != ".env.local" {
		t.Errorf("wrong origin file: got %q", got)
	}
}

// The directory you stand in must not decide which credentials you send.
func TestLoadDotEnvFilesOffByDefault(t *testing.T) {
	chdirToDotEnv(t, ".env", "MYAPP_API_KEY=from-dotenv\n")

	clearDotEnvEnv(t, "MYAPP_API_KEY")

	loadDotEnvFiles("MYAPP", "")

	if got := os.Getenv("MYAPP_API_KEY"); got != "" {
		t.Errorf("dotenv was read without being enabled: got %q", got)
	}
}

// A non-boolean value is an error, not a silent "off": this gates credentials.
// A config-file key is not consulted at all, by design.
func TestDotEnvEnabled(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    bool
		wantErr bool
	}{
		{value: "", want: false},
		{value: "1", want: true},
		{value: "true", want: true},
		{value: " 1 ", want: true},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "yes", wantErr: true},
		{value: "on", wantErr: true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("MYAPP_DOTENV", tc.value)

			got, err := dotEnvEnabled("MYAPP")

			if (err != nil) != tc.wantErr {
				t.Fatalf("%q: error %v, wantErr %v", tc.value, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("%q: got %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// chdirToDotEnv returns the directory so a test can add a second file.
func chdirToDotEnv(t *testing.T, filename, contents string) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(dir+"/"+filename, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	return dir
}

// clearDotEnvEnv unsets the switch and the named variables for one test.
func clearDotEnvEnv(t *testing.T, keys ...string) {
	t.Helper()

	for _, key := range append([]string{"MYAPP_DOTENV"}, keys...) {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}
