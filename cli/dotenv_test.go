package cli

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/viper"
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

// scanDotEnvFile is the parser behind both the import and the candidate paths,
// and every other fixture here is a bare KEY=value, so the dialect it accepts
// is pinned once, here.
func TestScanDotEnvFileParsesTheDotEnvDialect(t *testing.T) {
	chdirToDotEnv(t, ".env", strings.Join([]string{
		"# a comment",
		"",
		"   ",
		"PLAIN=value",
		"export EXPORTED=exported-value",
		`DOUBLE="double quoted"`,
		"SINGLE='single quoted'",
		"SPACED   =   padded   ",
		"EMPTY=",
		"NO_EQUALS",
		"=no-key",
		`UNBALANCED="half`,
		"WITH_EQUALS=a=b",
	}, "\n")+"\n")

	found := map[string]string{}
	if err := scanDotEnvFile(".env", func(key, value string) {
		found[key] = value
	}); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"PLAIN":       "value",
		"EXPORTED":    "exported-value",
		"DOUBLE":      "double quoted",
		"SINGLE":      "single quoted",
		"SPACED":      "padded",
		"EMPTY":       "",
		"UNBALANCED":  `"half`,
		"WITH_EQUALS": "a=b",
	}
	if !reflect.DeepEqual(found, want) {
		t.Errorf("parsed %#v, want %#v", found, want)
	}
}

// An absent file is the normal case and not an error; anything else is, or an
// unreadable .env would look exactly like one that was never there.
func TestScanDotEnvFileReportsOnlyRealFailures(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := scanDotEnvFile(".env", func(string, string) {}); err != nil {
		t.Fatalf("an absent file is not a failure: %v", err)
	}

	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}

	if err := os.WriteFile(".env", []byte("MYAPP_API_KEY=x\n"), 0000); err != nil {
		t.Fatal(err)
	}
	if err := scanDotEnvFile(".env", func(string, string) {}); err == nil {
		t.Error("an unreadable file was reported as a clean read")
	}
}

// The candidate is advice, so every state where acting on it would change
// nothing must report nothing: the switch already on, a value that would not
// resolve the key anyway, a variable the environment already supplies, and a
// switch the CLI could not parse in the first place.
func TestDotEnvCandidateOnlyReportsActionableKeys(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contents string
		setup    func(t *testing.T)
		wantFile string
	}{
		{
			name:     "off and the file defines the key",
			contents: "MYAPP_API_KEY=from-dotenv\n",
			wantFile: ".env",
		},
		{
			name:     "loading already on",
			contents: "MYAPP_API_KEY=from-dotenv\n",
			setup:    func(t *testing.T) { t.Setenv("MYAPP_DOTENV", "1") },
		},
		{
			name:     "switch is unparseable",
			contents: "MYAPP_API_KEY=from-dotenv\n",
			setup:    func(t *testing.T) { t.Setenv("MYAPP_DOTENV", "yes") },
		},
		{
			name:     "value would not resolve the key",
			contents: "MYAPP_API_KEY=\n",
		},
		{
			name:     "environment already supplies it",
			contents: "MYAPP_API_KEY=from-dotenv\n",
			setup:    func(t *testing.T) { t.Setenv("MYAPP_API_KEY", "exported") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chdirToDotEnv(t, ".env", tc.contents)
			clearDotEnvEnv(t, "MYAPP_API_KEY")

			viper.Set("env-prefix", "MYAPP")
			t.Cleanup(func() { viper.Set("env-prefix", nil) })
			if tc.setup != nil {
				tc.setup(t)
			}

			file, key := DotEnvCandidate([]string{"MYAPP_API_KEY"})

			if file != tc.wantFile {
				t.Errorf("file: got %q, want %q", file, tc.wantFile)
			}
			if tc.wantFile == "" && key != "" {
				t.Errorf("key: got %q, want no key", key)
			}
			if tc.wantFile != "" && key != "MYAPP_API_KEY" {
				t.Errorf("key: got %q, want MYAPP_API_KEY", key)
			}
			if got := os.Getenv("MYAPP_API_KEY"); got == "from-dotenv" {
				t.Error("reporting a candidate must not import it")
			}
		})
	}
}

// dotEnvFiles order decides which file the advice names; naming .env while the
// CLI would load .env.local would point the user at the wrong line.
func TestDotEnvCandidateFollowsTheFileOrder(t *testing.T) {
	dir := chdirToDotEnv(t, ".env", "MYAPP_API_KEY=from-dotenv\n")
	if err := os.WriteFile(dir+"/.env.local", []byte("MYAPP_API_KEY=from-local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	clearDotEnvEnv(t, "MYAPP_API_KEY")

	viper.Set("env-prefix", "MYAPP")
	t.Cleanup(func() { viper.Set("env-prefix", nil) })

	file, _ := DotEnvCandidate([]string{"MYAPP_API_KEY"})

	if file != dotEnvFiles[0] {
		t.Errorf("got %q, want the first file loadDotEnvFiles would read, %q", file, dotEnvFiles[0])
	}
}
