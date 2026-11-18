package providers

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
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
