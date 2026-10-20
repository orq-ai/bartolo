package cli

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// credstore.go keeps API keys out of the plaintext credentials.json. A profile's
// api_key is a workspace credential: any process running as the user can read it
// from the 0600 file, as can the backup, sync, and crash-report tools that walk
// $HOME. This moves it into the OS secure store, keyed per profile, and leaves
// the non-secret profile metadata in the JSON so it stays inspectable. It mirrors
// orq-cli's own session store (RES-1682); the mechanics are identical, only the
// key (profile name, not session host) and the service name differ.
//
// Shelling out to the platform tool is deliberate: it needs no cgo and no new
// dependency. The write must not pass the secret in argv, where a same-user
// process could read it from the process table — the very threat the file
// permissions already fail against. macOS takes the command on stdin via
// `security -i`; Linux feeds the secret to `secret-tool` on stdin.

// secretService is the keychain service every profile key is filed under. It is
// the app name (viper "app-name", set by Init), so each embedding CLI has its
// own namespace and orq-cli's profile keys never collide with its session store
// (a different service). The account is the profile name.
func secretService() string {
	if name := strings.TrimSpace(viper.GetString("app-name")); name != "" {
		return name
	}
	return "bartolo"
}

// secretStoreForcedFile reports whether the app's <PREFIX>_CREDENTIAL_STORE is
// set to "file": secrets then stay in credentials.json (CI, containers, headless
// Linux with no Secret Service). viper binds it from the env under the app's
// prefix, so bartolo stays generic — orq-cli reads ORQ_CREDENTIAL_STORE without
// the name being hardcoded here.
func secretStoreForcedFile() bool {
	return strings.EqualFold(strings.TrimSpace(viper.GetString("credential-store")), "file")
}

// SecretStore persists one profile's api_key, keyed by profile name. Load reports
// ok=false (nil error) only when the store positively holds nothing for the
// profile. A store that cannot answer (locked keyring, unreachable service)
// returns an error, so a transient failure never reads as a missing key.
type SecretStore interface {
	Load(account string) (secret string, ok bool, err error)
	Save(account, secret string) error
	Delete(account string) error
	// Name is a short label for status output, e.g. "macOS Keychain".
	Name() string
}

// runSecretTool is the single hook tests replace to stand in for the platform
// binary without a real keychain. stdin is fed to the command; stdout and the
// trimmed stderr come back alongside the error, so a store can tell a positive
// "not found" (quiet non-zero exit) from a real failure (exit with a message).
var runSecretTool = func(stdin string, name string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return out.String(), strings.TrimSpace(errBuf.String()), err
}

// exitCoder is satisfied by *exec.ExitError (ExitCode() int) and by the fake a
// test injects, so the stores read a command's exit code without caring whether
// a real process produced it.
type exitCoder interface{ ExitCode() int }

// exitCode returns the process exit code carried by err, or -1 if err does not
// carry one (tool missing, I/O error).
func exitCode(err error) int {
	var ec exitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return -1
}

// toolInstalled reports whether name resolves on PATH, so resolution can fall
// back to the file store rather than failing every command when the secure
// store's binary is absent.
func toolInstalled(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// resolveSecretStore chooses the store for this invocation. It returns nil when
// api keys should stay in credentials.json: the escape hatch is "file", or no
// secure-store binary is available on this platform. It is a var so a test can
// inject a fake store without a real keychain or a platform dependency.
var resolveSecretStore = func() SecretStore {
	// Never touch the real OS keychain under `go test`: a profile write would
	// shell out to `security`/`secret-tool` and can block on a keychain prompt,
	// and this fires for every package that embeds bartolo, not just this one.
	// Tests that exercise the store inject a fake over this var directly.
	if testing.Testing() {
		return nil
	}
	if secretStoreForcedFile() {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		if toolInstalled("security") {
			return macKeychainStore{}
		}
	case "linux":
		// secret-tool needs a running Secret Service (gnome-keyring, KWallet).
		// A probe keeps a headless box from picking a store every call fails on.
		if toolInstalled("secret-tool") && secretToolReady() {
			return secretToolStore{}
		}
	}
	// No secure store on this platform (Windows and the rest): keys stay in the
	// 0600 credentials.json. A Windows credential store is out of scope here.
	return nil
}

// ---- macOS: security(1) / Keychain ----

type macKeychainStore struct{}

func (macKeychainStore) Name() string { return "macOS Keychain" }

func (macKeychainStore) Load(account string) (string, bool, error) {
	// Read chunk 0, then each continuation chunk, until one is missing (exit 44,
	// errSecItemNotFound). The joined value is base64 (see Save).
	var enc strings.Builder
	for i := 0; ; i++ {
		service, acct := chunkTarget(account, i)
		// -w prints only the password.
		out, stderr, err := runSecretTool("", "security", "find-generic-password",
			"-s", service, "-a", acct, "-w")
		if err != nil {
			if exitCode(err) == 44 {
				if i == 0 {
					return "", false, nil // no secret stored
				}
				break // no more chunks
			}
			return "", false, fmt.Errorf("security find-generic-password: %w: %s", err, stderr)
		}
		enc.WriteString(strings.TrimRight(out, "\n"))
	}
	dec, derr := base64.StdEncoding.DecodeString(enc.String())
	if derr != nil {
		return "", false, fmt.Errorf("keychain secret is not valid base64: %w", derr)
	}
	return string(dec), true, nil
}

// maxKeychainCommand bounds a single `security -i` command line. Observed on
// macOS: security's interactive reader takes each line into a 4095-byte buffer,
// and a longer line is split mid-command, so it stores a truncated value and
// runs the tail as a second, failing command that echoes the tail (base64
// secret) to stderr. So Save splits the encoded secret across keychain items,
// each written on a command line kept under this margin (4000, leaving headroom
// under the observed 4095). The secret is never truncated and never falls back
// to plaintext just for being large.
const maxKeychainCommand = 4000

// addGenericPasswordLine builds the exact `security -i` line Save writes for one
// chunk. Both the per-chunk budget (with an empty value) and the real command use
// it, so the overhead the budget subtracts can never drift from the command sent.
func addGenericPasswordLine(service, account, value string) string {
	return fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", service, account, value)
}

// keychainContService holds continuation chunks for a secret too large for one
// keychain item. Chunk 0 stays under the primary service with the bare account
// (so a one-item secret is read by a plain lookup); chunk i>=1 lives here as
// "<account>.<i>", a separate namespace so it never collides with a primary item.
func keychainContService() string { return secretService() + ".cont" }

// errSecretTooLarge is returned only in the degenerate case where the service
// and account names are themselves so long that not even an empty value fits one
// command line. It names no secret material.
var errSecretTooLarge = errors.New("keychain service/account names too long for the command interface")

// chunkTarget is the (service, account) a given chunk of the secret is stored
// under. See keychainContService for why chunk 0 is special.
func chunkTarget(account string, i int) (service, acct string) {
	if i == 0 {
		return secretService(), account
	}
	return keychainContService(), fmt.Sprintf("%s.%d", account, i)
}

func (macKeychainStore) Save(account, secret string) error {
	// base64 so the blob carries no spaces or quotes to break the interactive
	// parser, and the command goes in on stdin via `security -i`, not argv, so
	// the token never appears in the process table. -U updates in place.
	enc := base64.StdEncoding.EncodeToString([]byte(secret))

	var chunks []string
	rest := enc
	for i := 0; ; i++ {
		service, acct := chunkTarget(account, i)
		overhead := len(addGenericPasswordLine(service, acct, ""))
		budget := maxKeychainCommand - overhead
		if budget <= 0 {
			return errSecretTooLarge // names so long no value fits a line
		}
		if len(rest) <= budget {
			chunks = append(chunks, rest)
			break
		}
		chunks = append(chunks, rest[:budget])
		rest = rest[budget:]
	}

	for i, chunk := range chunks {
		service, acct := chunkTarget(account, i)
		cmd := addGenericPasswordLine(service, acct, chunk)
		if _, _, err := runSecretTool(cmd, "security", "-i"); err != nil {
			// Best-effort: drop every chunk so Load never finds a half-written
			// secret (it would fail to base64-decode) and no stale tail fragment
			// from a previous larger secret is orphaned.
			for j := 0; ; j++ {
				s, a := chunkTarget(account, j)
				_, _, derr := runSecretTool("", "security", "delete-generic-password", "-s", s, "-a", a)
				if derr != nil && j > i {
					break // past our writes and no more leftover chunks
				}
			}
			// Never include security's stderr: on a rejected `-i` line it echoes
			// the command, which carries the base64 secret. Exit code only.
			return fmt.Errorf("security -i add-generic-password failed (exit %d)", exitCode(err))
		}
	}

	// Clear any leftover chunks from a previous, larger secret. Stop at the first
	// not-found (exit 44). Any OTHER delete error could leave a stale tail chunk
	// that Load would stitch onto the new value and mis-decode, so remove chunk 0
	// of what we just wrote and fail: Load then stops at the missing chunk 0 and
	// the caller keeps the key in the 0600 file, rather than reading a corrupt join.
	for i := len(chunks); ; i++ {
		service, acct := chunkTarget(account, i)
		_, _, err := runSecretTool("", "security", "delete-generic-password", "-s", service, "-a", acct)
		if err == nil {
			continue
		}
		if exitCode(err) == 44 {
			break // no more leftover chunks
		}
		for j := range chunks {
			s, a := chunkTarget(account, j)
			_, _, _ = runSecretTool("", "security", "delete-generic-password", "-s", s, "-a", a)
		}
		return fmt.Errorf("security delete-generic-password: could not clear a leftover keychain chunk (exit %d)", exitCode(err))
	}
	return nil
}

func (macKeychainStore) Delete(account string) error {
	// Remove chunk 0 and every continuation chunk. A missing chunk 0 does NOT end
	// the removal: a prior partial write can leave chunk 0 gone while continuations
	// remain. Only a missing continuation (i >= 1) ends the tail. A real error
	// stops the scan and is returned, since the rest cannot be cleared anyway.
	for i := 0; ; i++ {
		service, acct := chunkTarget(account, i)
		_, stderr, err := runSecretTool("", "security", "delete-generic-password",
			"-s", service, "-a", acct)
		if err == nil {
			continue
		}
		if exitCode(err) == 44 {
			if i == 0 {
				continue // chunk 0 already gone, but continuations may remain
			}
			return nil // a missing continuation ends the tail
		}
		return fmt.Errorf("security delete-generic-password: %w: %s", err, stderr)
	}
}

// manualClearHint is the exact command a user can run to remove a store entry by
// hand, for when a best-effort Delete could not (an unreachable or locked store).
// It names chunk 0, the primary item; empty for the file fallback.
func manualClearHint(store SecretStore, account string) string {
	switch store.(type) {
	case macKeychainStore:
		return fmt.Sprintf("security delete-generic-password -s %s -a %s", secretService(), account)
	case secretToolStore:
		return fmt.Sprintf("secret-tool clear service %s account %s", secretService(), account)
	}
	return ""
}

// ---- Linux: secret-tool(1) / libsecret ----

type secretToolStore struct{}

func (secretToolStore) Name() string { return "Linux secret-tool (libsecret)" }

func (secretToolStore) Load(account string) (string, bool, error) {
	out, stderr, err := runSecretTool("", "secret-tool", "lookup",
		"service", secretService(), "account", account)
	if err != nil {
		// secret-tool exits 1 for a missing item (quiet) and also when it cannot
		// reach the Secret Service (stderr set). Only the quiet exit 1 is "not
		// found"; anything with a message is a real error the caller must see.
		if exitCode(err) == 1 && stderr == "" {
			return "", false, nil
		}
		return "", false, fmt.Errorf("secret-tool lookup: %w: %s", err, stderr)
	}
	return out, true, nil
}

func (secretToolStore) Save(account, secret string) error {
	// The secret is fed on stdin, never argv.
	_, stderr, err := runSecretTool(secret, "secret-tool", "store",
		"--label", secretService()+" "+account,
		"service", secretService(), "account", account)
	if err != nil {
		return fmt.Errorf("secret-tool store: %w: %s", err, stderr)
	}
	return nil
}

func (secretToolStore) Delete(account string) error {
	_, stderr, err := runSecretTool("", "secret-tool", "clear",
		"service", secretService(), "account", account)
	if err != nil {
		return fmt.Errorf("secret-tool clear: %w: %s", err, stderr)
	}
	return nil
}

// secretToolReady confirms a Secret Service is actually answering, not just that
// the binary exists: on a headless box the daemon is often absent, and a lookup
// then errors. A lookup for a name nothing stores returns a quiet exit 1 when the
// service is up ("no such item"); an exit 1 with stderr, or any other code, means
// it could not be reached, so the file store is the safer pick.
func secretToolReady() bool {
	_, stderr, err := runSecretTool("", "secret-tool", "lookup", "service", secretService(), "account", "__probe__")
	if err == nil {
		return true // the (empty) probe somehow exists; service is up
	}
	return exitCode(err) == 1 && stderr == ""
}
