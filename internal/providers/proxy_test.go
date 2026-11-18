package providers

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/gauge/internal/store"
)

func TestResolveProxySetting(t *testing.T) {
	cases := []struct {
		setting string
		url     string
		source  string
		direct  bool
	}{
		{"", "", "none", true},
		{"off", "", "setting", true},
		{"Direct", "", "setting", true},
		{"http://127.0.0.1:7890", "http://127.0.0.1:7890", "setting", false},
		{"127.0.0.1:7897", "http://127.0.0.1:7897", "setting", false},
		{"socks5://127.0.0.1:7891", "socks5://127.0.0.1:7891", "setting", false},
	}
	for _, c := range cases {
		t.Run(c.setting, func(t *testing.T) {
			// An empty HTTP(S)_PROXY keeps the machine's own proxy out of the test.
			t.Setenv("HTTPS_PROXY", "")
			t.Setenv("https_proxy", "")
			t.Setenv("HTTP_PROXY", "")
			t.Setenv("http_proxy", "")
			t.Setenv("ALL_PROXY", "")
			t.Setenv("all_proxy", "")
			got := ResolveProxy(c.setting)
			if got.Setting != c.setting {
				t.Fatalf("setting = %q", got.Setting)
			}
			if got.URL != c.url || got.Source != c.source || got.Direct != c.direct {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestResolveProxyEnvironment(t *testing.T) {
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("HTTPS_PROXY", "127.0.0.1:6152")
	got := ResolveProxy("")
	if got.URL != "http://127.0.0.1:6152" || got.Source != "environment" || got.Direct {
		t.Fatalf("got %+v", got)
	}
}

func TestValidateProxy(t *testing.T) {
	for _, ok := range []string{"", "off", "direct", "http://127.0.0.1:7890", "127.0.0.1:7890", "socks5://127.0.0.1:7891", "https://proxy.example:443"} {
		if err := ValidateProxy(ok); err != nil {
			t.Fatalf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"ftp://127.0.0.1:7890", "127.0.0.1", "http://127.0.0.1:port"} {
		if err := ValidateProxy(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestParseScutilProxy(t *testing.T) {
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 7891
  SOCKSProxy : 127.0.0.1
}`
	if got := parseScutilProxy(out); got != "http://127.0.0.1:7890" {
		t.Fatalf("got %q", got)
	}
	// HTTPS disabled: fall through to the SOCKS entry, which Clash exposes too.
	disabled := strings.Replace(out, "HTTPSEnable : 1", "HTTPSEnable : 0", 1)
	if got := parseScutilProxy(disabled); got != "http://127.0.0.1:7890" {
		t.Fatalf("http fallback got %q", got)
	}
	noProxy := "HTTPEnable : 0\nHTTPSEnable : 0\nSOCKSEnable : 1\nSOCKSPort : 7891\nSOCKSProxy : 127.0.0.1\n"
	if got := parseScutilProxy(noProxy); got != "socks5://127.0.0.1:7891" {
		t.Fatalf("socks got %q", got)
	}
	if got := parseScutilProxy("HTTPEnable : 0\n"); got != "" {
		t.Fatalf("expected no proxy, got %q", got)
	}
}

func TestProxySkipsLoopbackAndFollowsSetting(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir+"/q.db", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	p := NewProxy(st)
	hook := p.hook()

	// Default route is on the machine's own terms; loopback never proxies.
	local, _ := url.Parse("http://127.0.0.1:1234/rpc")
	if got, err := hook(&http.Request{URL: local}); err != nil || got != nil {
		t.Fatalf("loopback: %v %v", got, err)
	}
	remote, _ := url.Parse("https://oauth2.googleapis.com/token")
	if _, err := hook(&http.Request{URL: remote}); err != nil {
		t.Fatalf("remote: %v", err)
	}

	if err := st.Set(ctx, ProxySetting, "http://127.0.0.1:7890"); err != nil {
		t.Fatal(err)
	}
	p.Invalidate()
	if got := p.Config(ctx); got.URL != "http://127.0.0.1:7890" {
		t.Fatalf("config = %+v", got)
	}
	proxied, err := hook(&http.Request{URL: remote})
	if err != nil || proxied == nil || proxied.Host != "127.0.0.1:7890" {
		t.Fatalf("remote through setting: %v %v", proxied, err)
	}
	if got, err := hook(&http.Request{URL: local}); err != nil || got != nil {
		t.Fatalf("loopback through setting: %v %v", got, err)
	}

	// "off" is an explicit direct connection, never the environment.
	if err := st.Set(ctx, ProxySetting, "off"); err != nil {
		t.Fatal(err)
	}
	p.Invalidate()
	if got := p.Config(ctx); !got.Direct || got.URL != "" {
		t.Fatalf("off config = %+v", got)
	}
	if got, _ := hook(&http.Request{URL: remote}); got != nil {
		t.Fatalf("off still proxied %v", got)
	}
}

func TestProxyClientTimeout(t *testing.T) {
	p := NewProxy(nil)
	if c := p.Client(3 * time.Second); c.Timeout != 3*time.Second {
		t.Fatalf("timeout = %v", c.Timeout)
	}
}
