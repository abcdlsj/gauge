package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseAndCompareSemver(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"v0.3.1", "0.3.1", true},
		{"0.3.1", "0.3.1", true},
		{"v0.3.1-3-gabc1234-dirty", "0.3.1", true},
		{"v1.2", "1.2.0", true},
		{"dev", "", false},
		{"abc1234", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		v, ok := parseSemver(tc.raw)
		if ok != tc.ok {
			t.Errorf("parseSemver(%q) ok = %v, want %v", tc.raw, ok, tc.ok)
			continue
		}
		if ok && v.String() != tc.want {
			t.Errorf("parseSemver(%q) = %s, want %s", tc.raw, v.String(), tc.want)
		}
	}

	a, _ := parseSemver("v0.3.1")
	b, _ := parseSemver("v0.3.2")
	c, _ := parseSemver("v0.10.0")
	d, _ := parseSemver("v1.0.0")
	if compareSemver(a, b) >= 0 {
		t.Errorf("expected %s < %s", a, b)
	}
	if compareSemver(b, c) >= 0 {
		t.Errorf("expected %s < %s", b, c)
	}
	if compareSemver(c, d) >= 0 {
		t.Errorf("expected %s < %s", c, d)
	}
	if compareSemver(a, a) != 0 {
		t.Errorf("expected %s == %s", a, a)
	}
}

func TestParseSHA256Sums(t *testing.T) {
	sum := sha256.Sum256([]byte("hello gauge"))
	hexSum := hex.EncodeToString(sum[:])

	content := fmt.Sprintf(
		"%s  Gauge-0.3.2-macos-universal.zip\n%s *Gauge-0.3.2-macos-universal.dmg\n",
		hexSum, hexSum,
	)

	got, err := parseSHA256Sums(content, "Gauge-0.3.2-macos-universal.zip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != hexSum {
		t.Fatalf("got %s, want %s", got, hexSum)
	}

	gotDmg, err := parseSHA256Sums(content, "Gauge-0.3.2-macos-universal.dmg")
	if err != nil || gotDmg != hexSum {
		t.Fatalf("expected binary-mode * entry to parse: got=%q err=%v", gotDmg, err)
	}

	if _, err := parseSHA256Sums(content, "missing.zip"); err == nil {
		t.Fatalf("expected error for missing file")
	}
}

func TestCheckAndETag(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/repos/abcdlsj/gauge/releases/latest" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") == `"etag-v032"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"etag-v032"`)
		_ = json.NewEncoder(w).Encode(githubRelease{
			TagName: "v0.3.2",
			HTMLURL: "https://github.com/abcdlsj/gauge/releases/tag/v0.3.2",
			Assets: []releaseAsset{
				{Name: "Gauge-0.3.2-macos-universal.zip", BrowserDownloadURL: "https://example.test/zip", Size: 1024},
				{Name: "SHA256SUMS.txt", BrowserDownloadURL: "https://example.test/sums", Size: 128},
			},
		})
	}))
	defer srv.Close()

	m := New(Config{
		CurrentVersion: "v0.3.1",
		APIBaseURL:     srv.URL,
		EnableApply:    true,
		detectAppFn:    func() (string, bool) { return "/Applications/Gauge.app", true },
	})

	st := m.Check(context.Background())
	if !st.Available || st.Latest != "0.3.2" || st.State != StateAvailable || !st.CanApply {
		t.Fatalf("unexpected status after first check: %+v", st)
	}

	// Second check should hit 304 Not Modified and preserve available state.
	st2 := m.Check(context.Background())
	if !st2.Available || st2.Latest != "0.3.2" || st2.State != StateAvailable {
		t.Fatalf("unexpected status after 304 check: %+v", st2)
	}
	if hits.Load() != 2 {
		t.Fatalf("expected 2 requests, got %d", hits.Load())
	}
}

func TestCheckUpToDateAndDraftIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRelease{
			TagName: "v0.3.1",
			HTMLURL: "https://github.com/abcdlsj/gauge/releases/tag/v0.3.1",
		})
	}))
	defer srv.Close()

	m := New(Config{
		CurrentVersion: "v0.3.1",
		APIBaseURL:     srv.URL,
	})
	st := m.Check(context.Background())
	if st.Available || st.State != StateIdle {
		t.Fatalf("expected idle/not available, got %+v", st)
	}
}

func TestApplyDownloadVerifyAndInstall(t *testing.T) {
	zipBytes := []byte("fake-zip-archive-payload-for-test")
	sum := sha256.Sum256(zipBytes)
	hexSum := hex.EncodeToString(sum[:])

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/abcdlsj/gauge/releases/latest":
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v0.3.2",
				HTMLURL: "https://github.com/abcdlsj/gauge/releases/tag/v0.3.2",
				Assets: []releaseAsset{
					{Name: "Gauge-0.3.2-macos-universal.zip", BrowserDownloadURL: srvURL + "/dl/zip", Size: int64(len(zipBytes))},
					{Name: "SHA256SUMS.txt", BrowserDownloadURL: srvURL + "/dl/sums", Size: 100},
				},
			})
		case "/dl/zip":
			_, _ = w.Write(zipBytes)
		case "/dl/sums":
			_, _ = fmt.Fprintf(w, "%s  Gauge-0.3.2-macos-universal.zip\n", hexSum)
		default:
			http.NotFound(w, r)
		}
	}))
	srvURL = srv.URL
	defer srv.Close()

	installedCh := make(chan string, 1)
	m := New(Config{
		CurrentVersion: "v0.3.1",
		APIBaseURL:     srv.URL,
		CacheDir:       t.TempDir(),
		EnableApply:    true,
		detectAppFn:    func() (string, bool) { return "/Applications/Gauge.app", true },
		installFn: func(_ context.Context, zipPath, _, targetApp string, _ func(), _ *log.Logger) error {
			data, err := os.ReadFile(zipPath)
			if err != nil {
				return err
			}
			if string(data) != string(zipBytes) {
				return fmt.Errorf("downloaded zip mismatch")
			}
			installedCh <- targetApp
			return nil
		},
	})

	st := m.Check(context.Background())
	if !st.Available || !st.CanApply {
		t.Fatalf("expected available & can_apply, got %+v", st)
	}
	if err := m.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	select {
	case target := <-installedCh:
		if target != "/Applications/Gauge.app" {
			t.Fatalf("unexpected targetApp: %s", target)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for installFn; status=%+v", m.Status())
	}
}

func TestApplyChecksumMismatchRecoversToError(t *testing.T) {
	zipBytes := []byte("corrupted-payload")
	badSum := hex.EncodeToString(make([]byte, 32))

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/abcdlsj/gauge/releases/latest":
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v0.3.2",
				HTMLURL: "https://github.com/abcdlsj/gauge/releases/tag/v0.3.2",
				Assets: []releaseAsset{
					{Name: "Gauge-0.3.2-macos-universal.zip", BrowserDownloadURL: srvURL + "/dl/zip", Size: int64(len(zipBytes))},
					{Name: "SHA256SUMS.txt", BrowserDownloadURL: srvURL + "/dl/sums", Size: 100},
				},
			})
		case "/dl/zip":
			_, _ = w.Write(zipBytes)
		case "/dl/sums":
			_, _ = fmt.Fprintf(w, "%s  Gauge-0.3.2-macos-universal.zip\n", badSum)
		default:
			http.NotFound(w, r)
		}
	}))
	srvURL = srv.URL
	defer srv.Close()

	cacheDir := t.TempDir()
	m := New(Config{
		CurrentVersion: "v0.3.1",
		APIBaseURL:     srv.URL,
		CacheDir:       cacheDir,
		EnableApply:    true,
		detectAppFn:    func() (string, bool) { return "/Applications/Gauge.app", true },
	})

	m.Check(context.Background())
	if err := m.Apply(); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := m.Status()
		if st.State == StateError {
			if st.Error == "" {
				t.Fatalf("expected non-empty error message")
			}
			// Corrupted zip should have been removed.
			if _, err := os.Stat(filepath.Join(cacheDir, "0.3.2", "Gauge-0.3.2-macos-universal.zip")); !os.IsNotExist(err) {
				t.Fatalf("expected corrupted zip to be deleted")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for error state; status=%+v", m.Status())
}
