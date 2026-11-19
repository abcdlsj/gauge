package providers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Real reply shape captured from Colab's GetUserInfo endpoint.
const userInfoFixture = `[3,1029.4718750292598,[[1,["H100","G4","A100","L4","T4"]],[2,["V6E1","V5E1"]]],[[1],[2]],0.08,1]`

func TestParseColabUserInfo(t *testing.T) {
	balance, accelerators, err := parseColabUserInfo([]byte(userInfoFixture))
	if err != nil {
		t.Fatal(err)
	}
	if balance < 1029 || balance > 1030 {
		t.Fatalf("balance = %v", balance)
	}
	if len(accelerators) != 7 {
		t.Fatalf("accelerators = %v", accelerators)
	}
	if accelerators[0] != "H100" {
		t.Fatalf("unexpected accelerators: %v", accelerators)
	}
}

func TestSapisidAuth(t *testing.T) {
	cookie := "SID=abc; SAPISID=secret; __Secure-1PAPISID=first; __Secure-3PAPISID=third"
	header, err := sapisidAuth(cookie, colabOrigin)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Unix()
	want := func(v string) string {
		sum := sha1.Sum([]byte(fmt.Sprintf("%d %s %s", ts, v, colabOrigin)))
		return hex.EncodeToString(sum[:])
	}
	expected := fmt.Sprintf("SAPISIDHASH %d_%s SAPISID1PHASH %d_%s SAPISID3PHASH %d_%s",
		ts, want("secret"), ts, want("first"), ts, want("third"))
	if header != expected {
		t.Fatalf("header = %q, want %q", header, expected)
	}
}

func TestSapisidAuthRequiresCookie(t *testing.T) {
	if _, err := sapisidAuth("SID=abc", colabOrigin); err == nil {
		t.Fatal("expected an error without SAPISID")
	}
}

func TestColabPausesOnExpiredAuth(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)

	var hits atomic.Int32
	var status atomic.Int32
	status.Store(http.StatusUnauthorized)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		code := int(status.Load())
		if code != http.StatusOK {
			http.Error(w, "nope", code)
			return
		}
		_, _ = w.Write([]byte(userInfoFixture))
	}))
	defer srv.Close()

	_ = d.Store.Set(ctx, "browser_cookies", "off")
	_ = d.Store.Set(ctx, "colab_endpoint", srv.URL)
	_ = d.Store.Set(ctx, "colab_cookie", "SID=abc; SAPISID=old")

	// First call hits the server and trips the expired pause.
	if _, err := (Colab{}).Collect(ctx, d); !errors.Is(err, ErrExpiredAuth) {
		t.Fatalf("first error = %v, want ErrExpiredAuth", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}

	// Second call with the same cookie must pause without making any HTTP request.
	if _, err := (Colab{}).Collect(ctx, d); !errors.Is(err, ErrExpiredAuth) {
		t.Fatalf("second error = %v, want ErrExpiredAuth", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits after paused call = %d, want 1", hits.Load())
	}

	// Updating the cookie clears the pause and succeeds.
	status.Store(http.StatusOK)
	_ = d.Store.Set(ctx, "colab_cookie", "SID=abc; SAPISID=fresh")
	snaps, err := (Colab{}).Collect(ctx, d)
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	if len(snaps) == 0 || hits.Load() != 2 {
		t.Fatalf("snaps=%d hits=%d, want >0 and 2", len(snaps), hits.Load())
	}
}

func TestColabRateLimited(t *testing.T) {
	ctx := context.Background()
	d := newTestDeps(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_ = d.Store.Set(ctx, "browser_cookies", "off")
	_ = d.Store.Set(ctx, "colab_endpoint", srv.URL)
	_ = d.Store.Set(ctx, "colab_cookie", "SID=abc; SAPISID=secret")

	if _, err := (Colab{}).Collect(ctx, d); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}
