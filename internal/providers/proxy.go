package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/gauge/internal/store"
)

// ProxySetting is the settings key holding the upstream proxy. Empty means
// automatic: HTTPS_PROXY / HTTP_PROXY first, then macOS' own system proxy, then
// a direct connection. "off" (or "direct" / "none") refuses a proxy, and
// anything else is a proxy URL.
//
// Automatic mode reads the macOS system proxy because a bundle launched from
// Finder inherits no shell environment: a Clash in "system proxy" mode sets
// scutil's state, not HTTPS_PROXY, and every Google call would time out.
const ProxySetting = "proxy_url"

// proxyCacheTTL is how long a resolved route survives before the setting and
// the system configuration are read again. Short enough that pressing Test in
// Settings takes effect immediately, long enough to keep scutil out of the hot
// path.
const proxyCacheTTL = 15 * time.Second

// probeURL is a tiny endpoint that only resolves where Google is reachable.
const probeURL = "https://www.google.com/generate_204"

// ProxyConfig is the resolved egress route, shown in Settings and used by the
// probe.
type ProxyConfig struct {
	Setting string `json:"setting"` // raw setting, "" when automatic
	URL     string `json:"url"`     // effective proxy; "" means direct
	Source  string `json:"source"`  // setting, environment, system, none
	Direct  bool   `json:"direct"`
}

// Describe is the one-line summary the settings page shows.
func (c ProxyConfig) Describe() string {
	switch {
	case c.URL != "" && c.Source == "environment":
		return c.URL + " (environment)"
	case c.URL != "" && c.Source == "system":
		return c.URL + " (system proxy)"
	case c.URL != "":
		return c.URL
	case c.Source == "setting":
		return "direct (proxy off)"
	default:
		return "direct"
	}
}

// ResolveProxy turns a stored setting into the route the collectors will use.
func ResolveProxy(setting string) ProxyConfig {
	s := strings.TrimSpace(setting)
	switch strings.ToLower(s) {
	case "":
		if u := envProxy(); u != "" {
			return ProxyConfig{Setting: s, URL: u, Source: "environment"}
		}
		if u := systemProxy(); u != "" {
			return ProxyConfig{Setting: s, URL: u, Source: "system"}
		}
		return ProxyConfig{Setting: s, Source: "none", Direct: true}
	case "off", "none", "direct":
		return ProxyConfig{Setting: s, Source: "setting", Direct: true}
	default:
		return ProxyConfig{Setting: s, URL: normalizeProxyURL(s), Source: "setting"}
	}
}

// ValidateProxy rejects a value a collector could not dial, so a typo shows up
// in the settings field instead of as a collector failure minutes later.
func ValidateProxy(raw string) error {
	s := strings.TrimSpace(raw)
	switch strings.ToLower(s) {
	case "", "off", "none", "direct":
		return nil
	}
	u, err := url.Parse(normalizeProxyURL(s))
	if err != nil {
		return errors.New("proxy must be a URL like http://127.0.0.1:7890")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
	if u.Hostname() == "" || u.Port() == "" {
		return errors.New("proxy needs a host and a port")
	}
	if _, err := strconv.Atoi(u.Port()); err != nil {
		return errors.New("proxy port must be a number")
	}
	return nil
}

// normalizeProxyURL accepts "127.0.0.1:7890" as shorthand for an HTTP proxy.
func normalizeProxyURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		return s
	}
	return "http://" + strings.TrimPrefix(s, "//")
}

// envProxy returns the first proxy named in the environment.
func envProxy() string {
	for _, key := range []string{
		"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "HTTP_PROXY", "http_proxy",
	} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return normalizeProxyURL(v)
		}
	}
	return ""
}

// systemProxy reads macOS' network proxy configuration. scutil is part of the
// base system, and the call is cheap enough to sit behind proxyCacheTTL.
func systemProxy() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return ""
	}
	return parseScutilProxy(string(out))
}

// parseScutilProxy picks HTTPS, then HTTP, then SOCKS out of `scutil --proxy`.
// Only enabled entries count; the socks fallback covers proxies that expose a
// single SOCKS port.
func parseScutilProxy(out string) string {
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		key, rest, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(rest)
	}
	for _, kind := range []struct {
		enable, host, port, scheme string
	}{
		{"HTTPSEnable", "HTTPSProxy", "HTTPSPort", "http"},
		{"HTTPEnable", "HTTPProxy", "HTTPPort", "http"},
		{"SOCKSEnable", "SOCKSProxy", "SOCKSPort", "socks5"},
	} {
		if values[kind.enable] != "1" {
			continue
		}
		host, port := values[kind.host], values[kind.port]
		if host == "" || port == "" || port == "0" {
			continue
		}
		return fmt.Sprintf("%s://%s", kind.scheme, net.JoinHostPort(host, port))
	}
	return ""
}

// isLoopbackHost reports whether a URL host is the local machine. Loopback
// calls (the IDE's language server) never go through the proxy.
func isLoopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// newTransport builds the transport every Gauge client shares: keep-alives,
// short timeouts, and a proxy decided per request.
func newTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy: proxy,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// Proxy resolves the stored route for the shared clients. It caches the result
// for proxyCacheTTL so a change in Settings applies on the next request without
// restarting the app or the collectors.
type Proxy struct {
	store *store.Store

	mu  sync.Mutex
	cfg ProxyConfig
	at  time.Time
}

// NewProxy returns a resolver backed by the settings table.
func NewProxy(st *store.Store) *Proxy { return &Proxy{store: st} }

// Config resolves the route, reading the setting only when the cache expired.
func (p *Proxy) Config(ctx context.Context) ProxyConfig {
	p.mu.Lock()
	if !p.at.IsZero() && time.Since(p.at) < proxyCacheTTL {
		cfg := p.cfg
		p.mu.Unlock()
		return cfg
	}
	p.mu.Unlock()

	raw := ""
	if p.store != nil {
		if v, _, err := p.store.Get(ctx, ProxySetting); err == nil {
			raw = v
		}
	}
	cfg := ResolveProxy(raw)

	p.mu.Lock()
	p.cfg, p.at = cfg, time.Now()
	p.mu.Unlock()
	return cfg
}

// Invalidate drops the cached route, so the next request re-reads settings.
func (p *Proxy) Invalidate() {
	p.mu.Lock()
	p.at = time.Time{}
	p.mu.Unlock()
}

// hook is the transport proxy function: loopback stays direct, everything else
// follows the resolved route.
func (p *Proxy) hook() func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if req != nil && req.URL != nil && isLoopbackHost(req.URL.Host) {
			return nil, nil
		}
		cfg := p.Config(req.Context())
		if cfg.Direct || cfg.URL == "" {
			return nil, nil
		}
		u, err := url.Parse(cfg.URL)
		if err != nil {
			return nil, err
		}
		return u, nil
	}
}

// Client returns a client whose routes follow the settings.
func (p *Proxy) Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: newTransport(p.hook()), Timeout: timeout}
}

// proxyFuncFor pins a config to a transport, for the probe.
func proxyFuncFor(cfg ProxyConfig) func(*http.Request) (*url.URL, error) {
	if cfg.Direct || cfg.URL == "" {
		return nil
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil
	}
	return func(*http.Request) (*url.URL, error) { return u, nil }
}

// Probe fetches the tiny Google endpoint through cfg and returns the round
// trip time. Any answer counts; only transport errors fail the probe.
func Probe(ctx context.Context, cfg ProxyConfig, timeout time.Duration) (time.Duration, error) {
	client := &http.Client{Transport: newTransport(proxyFuncFor(cfg)), Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Gauge")
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return time.Since(started), nil
}

// ProxyCandidate is one local proxy that reached Google.
type ProxyCandidate struct {
	URL       string `json:"url"`
	LatencyMS int64  `json:"latency_ms"`
}

// proxyPorts are the loopback ports local proxy clients listen on: Clash
// Verge / Party mixed (7890, 7897) and socks (7891), v2rayN (10808), Surge
// (6152), mitmproxy (8080), plus the usual suspects.
var proxyPorts = []int{7890, 7897, 7891, 1087, 1080, 10808, 6152, 8080, 8888, 8118}

// DetectProxies probes the usual loopback ports and returns the ones that
// reach Google, fastest first. A closed port costs one 250 ms dial, and the
// whole scan is bounded by the probe timeout.
func DetectProxies(ctx context.Context) []ProxyCandidate {
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		found []ProxyCandidate
	)
	for _, port := range proxyPorts {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
			if err != nil {
				return
			}
			_ = conn.Close()

			cand := ProxyCandidate{URL: "http://" + addr}
			latency, err := Probe(ctx, ProxyConfig{URL: cand.URL}, 3*time.Second)
			if err != nil {
				return
			}
			cand.LatencyMS = latency.Milliseconds()
			mu.Lock()
			found = append(found, cand)
			mu.Unlock()
		}(port)
	}
	wg.Wait()
	sort.Slice(found, func(i, j int) bool { return found[i].LatencyMS < found[j].LatencyMS })
	return found
}
