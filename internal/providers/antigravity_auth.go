package providers

import (
	"os"

	"github.com/abcdlsj/gauge/internal/antigravitykeychain"
)

// antigravityTokenFiles lists where Antigravity 2.x and the agy CLI keep a
// fallback copy of the sign-in. On macOS both clients use the Keychain item,
// but the files are still what a non-macOS agy writes, and what these
// collectors read.
var antigravityTokenFiles = []string{
	"~/.gemini/antigravity-cli/antigravity-oauth-token",
	"~/.gemini/jetski-standalone-oauth-token",
}

// antigravityLocalRefreshToken finds the refresh token the current clients own.
//
// ~/.gemini/oauth_creds.json is deliberately not consulted. Antigravity 2.x and
// agy stopped writing it, so whatever it still holds belongs to an older
// version or another tool; reading it reported an account the user had already
// signed out of.
//
// The macOS Keychain item is not read either: a read raises a system prompt, and
// an install that only watches quota should not see one. `ak agy export` is
// what writes that item.
func antigravityLocalRefreshToken() string {
	for _, path := range antigravityTokenFiles {
		if token := refreshTokenFromFile(expandHome(path)); token != "" {
			return token
		}
	}
	return ""
}

// refreshTokenFromFile parses one client token file.
func refreshTokenFromFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	cred, err := antigravitykeychain.Parse(raw)
	if err != nil {
		return ""
	}
	return cred.RefreshToken
}
