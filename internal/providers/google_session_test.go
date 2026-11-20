package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestParseListAccounts(t *testing.T) {
	// The shape ListAccounts returned for a real session (values replaced).
	body := []byte(`["gaia.l.a.r",[["gaia.l.a",1,"Bob","bob@x","https://lh3.googleusercontent.com/b",0,0,1,null,1,"2"],` +
		`["gaia.l.a",1,"Ann","ann@x","https://lh3.googleusercontent.com/a",1,1,0,null,1,"1"]]]`)
	got, err := parseListAccounts(body)
	if err != nil || got.Email != "ann@x" || got.Name != "Ann" || got.Picture == "" {
		t.Fatalf("got %+v, %v; want the authuser-0 account", got, err)
	}
	// Without indices the first entry stands for authuser 0.
	got, err = parseListAccounts([]byte(`["gaia.l.a.r",[["gaia.l.a",1,"Ann","ann@x"],["gaia.l.a",1,"Bob","bob@x"]]]`))
	if err != nil || got.Email != "ann@x" {
		t.Fatalf("got %+v, %v; want the first account", got, err)
	}
	for _, bad := range []string{`[]`, `["gaia.l.a.r",[]]`, `{"x":1}`, `["gaia.l.a.r",[["other",1,"n","no-at"]]]`} {
		if _, err := parseListAccounts([]byte(bad)); err == nil {
			t.Fatalf("%s: want an error", bad)
		}
	}
}

// TestSessionAccountIsAskedOncePerCookie checks the lookup is cached against
// the cookie fingerprint and repeated when the cookie changes.
func TestSessionAccountIsAskedOncePerCookie(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "GET is a 400 upstream", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		email := "a@x"
		if r.Header.Get("Cookie") == "SID=2" {
			email = "b@x"
		}
		_, _ = w.Write([]byte(`["gaia.l.a.r",[["gaia.l.a",1,"N","` + email + `"]]]`))
	}))
	t.Cleanup(srv.Close)
	orig := listAccountsEndpoint
	listAccountsEndpoint = srv.URL
	t.Cleanup(func() { listAccountsEndpoint = orig })

	for range 2 {
		if got := d.sessionAccount(ctx, "colab", "SID=1"); got.Email != "a@x" {
			t.Fatalf("got %+v", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("asked %d times for one cookie", calls.Load())
	}
	if got := d.sessionAccount(ctx, "colab", "SID=2"); got.Email != "b@x" {
		t.Fatalf("new cookie kept the old account: %+v", got)
	}
	if got := SessionAccount(ctx, d.Store, "colab"); got.Email != "b@x" {
		t.Fatalf("stored = %+v", got)
	}
}
