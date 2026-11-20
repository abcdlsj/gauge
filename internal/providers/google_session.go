package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/abcdlsj/gauge/internal/model"
	"github.com/abcdlsj/gauge/internal/store"
)

// The Google account behind a browser cookie.
//
// Flow and Colab read the first account of the Chrome session (X-Goog-AuthUser
// 0) and their replies do not say which one that is. ListAccounts is what
// Chrome itself calls to name the accounts of a session; asked with the same
// cookie, its first entry is the account Flow and Colab report for. The panel
// uses it to put the two cards under the Google account they belong to.

// listAccountsEndpoint is a variable so tests can point it at a local server.
var listAccountsEndpoint = "https://accounts.google.com/ListAccounts?gpsia=1&source=ChromiumBrowser&json=standard"

// SessionAvatarSlot is the avatar path segment of the browser session's
// account (/api/avatar/web).
const SessionAvatarSlot = "web"

// SessionAccountKey is where a provider's cookie identity is kept, tagged with
// the cookie fingerprint so it is asked once per session.
func SessionAccountKey(provider string) string { return provider + "_session_account" }

// sessionAccount names the account a cookie signs in as. Failures return the
// zero Account and are retried on the next collection.
func (d *Deps) sessionAccount(ctx context.Context, provider, cookie string) model.Account {
	fp := shortFingerprint(cookie)
	key := SessionAccountKey(provider)
	if stored := d.loadIdentity(ctx, key); stored.FP == fp && stored.Email != "" {
		return stored.Account
	}

	account, err := d.listAccounts(ctx, cookie)
	if err != nil {
		d.Log.Printf("%s: cannot tell which Google account the cookie belongs to: %v", provider, err)
		_ = d.Store.Delete(ctx, key)
		return model.Account{}
	}
	account.Picture = d.avatarData(ctx, account.Picture)
	d.saveIdentity(ctx, key, storedIdentity{Account: account, FP: fp})
	return account
}

func (d *Deps) listAccounts(ctx context.Context, cookie string) (model.Account, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listAccountsEndpoint, nil)
	if err != nil {
		return model.Account{}, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0 Safari/537.36")
	resp, err := d.Client.Do(req)
	if err != nil {
		return model.Account{}, err
	}
	defer resp.Body.Close()
	body, err := readLimited(resp.Body, 256<<10)
	if err != nil {
		return model.Account{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return model.Account{}, fmt.Errorf("ListAccounts: HTTP %d", resp.StatusCode)
	}
	return parseListAccounts(body)
}

// parseListAccounts reads the first account of the positional reply:
//
//	["gaia.l.a.r", [["gaia.l.a", 1, "<name>", "<email>", "<photo>", ...], ...]]
func parseListAccounts(body []byte) (model.Account, error) {
	var top []json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || len(top) < 2 {
		return model.Account{}, fmt.Errorf("ListAccounts: unexpected reply")
	}
	var accounts [][]json.RawMessage
	if err := json.Unmarshal(top[1], &accounts); err != nil {
		return model.Account{}, fmt.Errorf("ListAccounts: unexpected account list")
	}
	for _, entry := range accounts {
		if len(entry) < 4 {
			continue
		}
		str := func(i int) string {
			var s string
			if i < len(entry) {
				_ = json.Unmarshal(entry[i], &s)
			}
			return s
		}
		if str(0) != "gaia.l.a" || !strings.Contains(str(3), "@") {
			continue
		}
		return model.Account{Name: str(2), Email: str(3), Picture: str(4)}, nil
	}
	return model.Account{}, fmt.Errorf("ListAccounts: no signed-in account")
}

// SessionAccount reads the stored cookie identity of a provider.
func SessionAccount(ctx context.Context, st *store.Store, provider string) model.Account {
	var id storedIdentity
	if raw, ok, err := st.Get(ctx, SessionAccountKey(provider)); err == nil && ok {
		_ = json.Unmarshal([]byte(raw), &id)
	}
	return id.Account
}

// tagSession stamps the cookie's account on a provider's readings.
func (d *Deps) tagSession(ctx context.Context, provider, cookie string, snaps []model.Snapshot) {
	email := d.sessionAccount(ctx, provider, cookie).Email
	for i := range snaps {
		snaps[i].Account = email
	}
}
