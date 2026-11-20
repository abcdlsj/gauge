package providers

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAntigravityLocalRefreshTokenSkipsTheRetiredFile checks the collector no
// longer reports the account that ~/.gemini/oauth_creds.json still names.
func TestAntigravityLocalRefreshTokenSkipsTheRetiredFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	gemini := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(gemini, 0o700); err != nil {
		t.Fatal(err)
	}
	retired := filepath.Join(gemini, "oauth_creds.json")
	if err := os.WriteFile(retired, []byte(`{"refresh_token":"retired"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if token := antigravityLocalRefreshToken(); token != "" {
		t.Fatalf("token = %q; oauth_creds.json must not be read", token)
	}

	// The agy token file is the supported fallback.
	cliDir := filepath.Join(gemini, "antigravity-cli")
	if err := os.MkdirAll(cliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	agy := filepath.Join(cliDir, "antigravity-oauth-token")
	if err := os.WriteFile(agy, []byte(`{"token":{"refresh_token":"from-agy"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if token := antigravityLocalRefreshToken(); token != "from-agy" {
		t.Fatalf("token = %q, want the agy file", token)
	}

	// The IDE's own fallback file is used when agy has none.
	if err := os.Remove(agy); err != nil {
		t.Fatal(err)
	}
	standalone := filepath.Join(gemini, "jetski-standalone-oauth-token")
	if err := os.WriteFile(standalone, []byte(`{"token":{"refresh_token":"from-ide"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if token := antigravityLocalRefreshToken(); token != "from-ide" {
		t.Fatalf("token = %q, want the IDE fallback file", token)
	}
}
