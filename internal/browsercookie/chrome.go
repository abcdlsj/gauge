// Package browsercookie reads cookies from a local Chromium profile.
//
// On macOS Chrome encrypts cookie values with AES-128-CBC (prefix "v10"). The
// key is derived from the "Chrome Safe Storage" keychain item with
// PBKDF2-SHA1(password, "saltysalt", 1003, 16). Reading that item triggers a
// one-time macOS authorisation prompt; the derived key is cached in memory only.
//
// Cookies are never persisted by this package: callers get an HTTP Cookie
// header value, use it, and drop it.
package browsercookie

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Chrome is a reader for one Chromium profile.
type Chrome struct {
	profile string

	mu  sync.Mutex
	key []byte
}

// ErrUnavailable is returned when Chrome or its cookies cannot be read.
var ErrUnavailable = fmt.Errorf("chrome cookies unavailable")

// NewChrome locates the Chromium profile that was used last.
func NewChrome() (*Chrome, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("%w: only macOS keychain access is implemented", ErrUnavailable)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, ErrUnavailable
	}
	base := filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	if _, err := os.Stat(base); err != nil {
		return nil, fmt.Errorf("%w: no Chrome profile at %s", ErrUnavailable, base)
	}
	return &Chrome{profile: pickProfile(base)}, nil
}

// Profile returns the directory name in use, e.g. "Default" or "Profile 1".
func (c *Chrome) Profile() string { return filepath.Base(c.profile) }

// pickProfile honours Chrome's own last-used marker and falls back to Default.
func pickProfile(base string) string {
	raw, err := os.ReadFile(filepath.Join(base, "Local State"))
	if err == nil {
		var state struct {
			Profile struct {
				LastUsed  string                           `json:"last_used"`
				InfoCache map[string]struct{ Name string } `json:"info_cache"`
			} `json:"profile"`
		}
		if json.Unmarshal(raw, &state) == nil {
			if dir := state.Profile.LastUsed; dir != "" {
				if _, err := os.Stat(filepath.Join(base, dir, "Cookies")); err == nil {
					return filepath.Join(base, dir)
				}
			}
			names := make([]string, 0, len(state.Profile.InfoCache))
			for name := range state.Profile.InfoCache {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if _, err := os.Stat(filepath.Join(base, name, "Cookies")); err == nil {
					return filepath.Join(base, name)
				}
			}
		}
	}
	return filepath.Join(base, "Default")
}

// Header returns a Cookie header value for the given registrable domain, e.g.
// "google.com". Only the auth-carrying cookie names are included to keep the
// request small.
func (c *Chrome) Header(domain string, allowed []string) (string, error) {
	rows, err := c.rows(domain)
	if err != nil {
		return "", err
	}

	var allow map[string]bool
	if len(allowed) > 0 {
		allow = make(map[string]bool, len(allowed))
		for _, name := range allowed {
			allow[name] = true
		}
	}

	// Chrome stores the same name under several host keys. Duplicates make
	// Google reject the whole header, so keep the most specific live entry.
	best := make(map[string]cookieRow)
	for _, row := range rows {
		if allow != nil && !allow[row.name] {
			continue
		}
		if Expired(row.expiry) {
			continue
		}
		if cur, ok := best[row.name]; !ok || len(row.host) > len(cur.host) {
			best[row.name] = row
		}
	}

	names := make([]string, 0, len(best))
	for name := range best {
		names = append(names, name)
	}
	sort.Strings(names)

	var parts []string
	for _, name := range names {
		value, err := c.value(best[name])
		if err != nil || value == "" {
			continue
		}
		parts = append(parts, name+"="+value)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("%w: no cookies for %s", ErrUnavailable, domain)
	}
	return strings.Join(parts, "; "), nil
}

// All returns every readable cookie for the domain, used when probing an
// undocumented endpoint.
func (c *Chrome) All(domain string) (string, error) {
	return c.Header(domain, nil)
}

type cookieRow struct {
	host   string
	name   string
	value  string
	enc    []byte
	expiry int64
}

// rows copies the cookie database (Chrome locks it while running) and reads it.
func (c *Chrome) rows(domain string) ([]cookieRow, error) {
	src := filepath.Join(c.profile, "Cookies")
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	tmp, err := os.MkdirTemp("", "gauge-cookies-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	for _, suffix := range []string{"", "-wal", "-shm"} {
		raw, err := os.ReadFile(src + suffix)
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(tmp, "Cookies"+suffix), raw, 0o600)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(tmp, "Cookies")+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	like := "%." + strings.TrimPrefix(domain, ".")
	q := `SELECT host_key, name, value, encrypted_value, expires_utc FROM cookies
	      WHERE host_key = ? OR host_key LIKE ?`
	rows, err := db.Query(q, "."+strings.TrimPrefix(domain, "."), like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []cookieRow
	for rows.Next() {
		var r cookieRow
		if err := rows.Scan(&r.host, &r.name, &r.value, &r.enc, &r.expiry); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// value decrypts one row, falling back to the plaintext column.
func (c *Chrome) value(r cookieRow) (string, error) {
	if len(r.enc) == 0 {
		return r.value, nil
	}
	key, err := c.storageKey()
	if err != nil {
		return "", err
	}
	return decrypt(key, r.enc, r.host)
}

// storageKey derives the AES key, caching it for the process lifetime.
func (c *Chrome) storageKey() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != nil {
		return c.key, nil
	}

	password, err := keychainPassword()
	if err != nil {
		return nil, err
	}
	c.key = pbkdf2SHA1([]byte(password), []byte("saltysalt"), 1003, 16)
	return c.key, nil
}

// keychainPassword asks macOS for Chrome's safe-storage password.
func keychainPassword() (string, error) {
	for _, args := range [][]string{
		{"find-generic-password", "-w", "-s", "Chrome Safe Storage", "-a", "Chrome"},
		{"find-generic-password", "-w", "-s", "Chrome Safe Storage"},
	} {
		out, err := exec.Command("security", args...).Output()
		if err == nil {
			return strings.TrimRight(string(out), "\n"), nil
		}
	}
	return "", fmt.Errorf("%w: keychain item not readable", ErrUnavailable)
}

// decrypt handles Chrome's "v10"/"v11" cookie format.
func decrypt(key, payload []byte, host string) (string, error) {
	if len(payload) < 3 || !bytes.HasPrefix(payload, []byte("v1")) {
		return string(payload), nil
	}
	body := payload[3:]
	if len(body) == 0 || len(body)%aes.BlockSize != 0 {
		return "", fmt.Errorf("ciphertext is not a multiple of the block size")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	out := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, bytes.Repeat([]byte(" "), aes.BlockSize)).CryptBlocks(out, body)

	if n := int(out[len(out)-1]); n > 0 && n <= aes.BlockSize && n <= len(out) {
		out = out[:len(out)-n]
	}

	// Chrome 80+ prepends SHA256(host_key) to the plaintext; strip it when it
	// matches so a format change cannot silently corrupt the value.
	if len(out) > 32 {
		sum := sha256.Sum256([]byte(host))
		if hmac.Equal(out[:32], sum[:]) {
			out = out[32:]
		}
	}
	return string(out), nil
}

// pbkdf2SHA1 is a minimal PBKDF2 implementation, avoiding a crypto dependency
// for the single use Chrome needs.
func pbkdf2SHA1(password, salt []byte, iter, keyLen int) []byte {
	hLen := sha1.Size
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	buf := make([]byte, 4)
	for block := 1; block <= blocks; block++ {
		mac := hmac.New(sha1.New, password)
		mac.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		mac.Write(buf)
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iter; i++ {
			mac = hmac.New(sha1.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// Expired reports whether a Chrome expiry timestamp is in the past. Chrome
// stores microseconds since 1601-01-01.
func Expired(expiry int64) bool {
	if expiry == 0 {
		return false
	}
	const epochDelta = 11644473600 // seconds between 1601 and 1970
	return time.Now().After(time.Unix(expiry/1e6-epochDelta, 0))
}
