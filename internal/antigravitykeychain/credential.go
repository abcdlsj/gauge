// Package antigravitykeychain reads and writes the macOS Keychain item that
// Antigravity 2.x and the agy CLI share: service "gemini", account
// "antigravity".
//
// The value is a "go-keyring-base64:" envelope around a small JSON document.
// This package owns that format because two callers need it: the collectors,
// which read the current sign-in, and the account switch, which replaces it.
package antigravitykeychain

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrUnsupported is returned on platforms without the shared Keychain item.
var ErrUnsupported = errors.New("the Antigravity Keychain item is only available on macOS")

// Credential is the signed-in identity as the clients store it.
type Credential struct {
	Email        string
	RefreshToken string
	AccessToken  string
	Expiry       time.Time
	IDToken      string
}

// payload mirrors the JSON the clients keep in the item.
type payload struct {
	Token struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		Expiry       string `json:"expiry,omitempty"`
		ExpiresIn    int    `json:"expires_in,omitempty"`
	} `json:"token"`
	AuthMethod   string `json:"auth_method,omitempty"`
	IDTokenEmail string `json:"id_token_email,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
}

// Parse decodes a stored item. The email comes from the explicit field first
// and falls back to the ID token claim.
func Parse(raw []byte) (Credential, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Credential{}, err
	}
	cred := Credential{
		Email:        p.IDTokenEmail,
		RefreshToken: p.Token.RefreshToken,
		AccessToken:  p.Token.AccessToken,
		IDToken:      p.IDToken,
	}
	if cred.Email == "" && cred.IDToken != "" {
		cred.Email = emailFromJWT(cred.IDToken)
	}
	if p.Token.Expiry != "" {
		if t, err := time.Parse(time.RFC3339Nano, p.Token.Expiry); err == nil {
			cred.Expiry = t
		}
	}
	return cred, nil
}

// Encode renders the item the clients expect. expires_in is derived, not
// carried over: the exact expiry timestamp is what the clients act on.
func Encode(c Credential) ([]byte, error) {
	var p payload
	p.Token.AccessToken = c.AccessToken
	p.Token.TokenType = "Bearer"
	p.Token.RefreshToken = c.RefreshToken
	if !c.Expiry.IsZero() {
		p.Token.Expiry = c.Expiry.Format(time.RFC3339Nano)
		p.Token.ExpiresIn = int(time.Until(c.Expiry).Seconds())
		if p.Token.ExpiresIn < 0 {
			p.Token.ExpiresIn = 0
		}
	}
	p.AuthMethod = "consumer"
	p.IDTokenEmail = c.Email
	p.IDToken = c.IDToken
	return json.Marshal(p)
}

// emailFromJWT reads the email claim without verifying the signature: the
// token came from the client's own Keychain item and is only used as a label.
func emailFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return ""
	}
	return claims.Email
}
