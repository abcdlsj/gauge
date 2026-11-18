package providers

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// avatarData downloads a Google profile picture and returns it as a data URI.
// The desktop webview loads pages from the app's own scheme, and a network
// image hop there is slow enough to fail; keeping the bytes local makes the
// avatar as cheap as the primary account's.
//
// Any failure returns the URL untouched: the avatar endpoint still proxies it.
func (d *Deps) avatarData(ctx context.Context, rawURL string) string {
	if !strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".googleusercontent.com") {
		return rawURL
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return rawURL
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := d.Client.Do(req)
	if err != nil {
		return rawURL
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rawURL
	}
	media := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(media, "image/") {
		return rawURL
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil || len(raw) == 0 {
		return rawURL
	}
	return "data:" + media + ";base64," + base64.StdEncoding.EncodeToString(raw)
}
