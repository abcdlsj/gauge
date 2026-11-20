package antigravitykeychain

import "testing"

func TestParseReadsTheClientsItem(t *testing.T) {
	raw := []byte(`{"token":{"access_token":"at","token_type":"Bearer","refresh_token":"rt","expiry":"2026-11-18T13:55:40.154251+08:00","expires_in":3599},"auth_method":"consumer","id_token_email":"a@x"}`)
	cred, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Email != "a@x" || cred.AccessToken != "at" || cred.RefreshToken != "rt" {
		t.Fatalf("cred = %+v", cred)
	}
	if cred.Expiry.IsZero() || cred.Expiry.Year() != 2026 {
		t.Fatalf("expiry = %v", cred.Expiry)
	}
}

// TestParseFallsBackToTheIDToken covers items written without the explicit
// email field.
func TestParseFallsBackToTheIDToken(t *testing.T) {
	// {"email":"a@x"} as an unsigned JWT payload segment.
	raw := []byte(`{"token":{"access_token":"at","refresh_token":"rt"},"id_token":"header.eyJlbWFpbCI6ImFAeCJ9.sig"}`)
	cred, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Email != "a@x" {
		t.Fatalf("email = %q", cred.Email)
	}
}

func TestEncodeRoundTrips(t *testing.T) {
	want := Credential{Email: "a@x", AccessToken: "at", RefreshToken: "rt", IDToken: "id"}
	raw, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != want.Email || got.AccessToken != want.AccessToken ||
		got.RefreshToken != want.RefreshToken || got.IDToken != want.IDToken {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestEmailFromJWT(t *testing.T) {
	if got := emailFromJWT("header.eyJlbWFpbCI6ImFAeCJ9.sig"); got != "a@x" {
		t.Fatalf("email = %q", got)
	}
	if got := emailFromJWT("nope"); got != "" {
		t.Fatalf("email = %q", got)
	}
}
