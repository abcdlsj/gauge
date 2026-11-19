// Package updater checks GitHub Releases for newer versions of Gauge,
// downloads and verifies the macOS bundle in the background, and hands off
// atomic replacement + silent restart to a detached helper process.
package updater

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultRepo          = "abcdlsj/gauge"
	DefaultAPIBaseURL    = "https://api.github.com"
	DefaultCheckInterval = 6 * time.Hour
	DefaultInitialDelay  = 20 * time.Second

	manualCheckCooldown = 30 * time.Second
	maxChecksumBytes    = 1 << 20   // 1 MiB
	maxAssetBytes       = 256 << 20 // 256 MiB
)

const (
	StateIdle        = "idle"
	StateChecking    = "checking"
	StateAvailable   = "available"
	StateDownloading = "downloading"
	StateInstalling  = "installing"
	StateError       = "error"
)

// Status is the JSON snapshot returned by GET /api/update.
type Status struct {
	Current    string `json:"current"`
	Latest     string `json:"latest,omitempty"`
	Available  bool   `json:"available"`
	State      string `json:"state"`
	Progress   int    `json:"progress"`
	CanApply   bool   `json:"can_apply"`
	ReleaseURL string `json:"release_url,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Config configures a Manager.
type Config struct {
	Repo           string
	CurrentVersion string
	APIBaseURL     string
	CheckInterval  time.Duration
	InitialDelay   time.Duration
	ClientFactory  func(timeout time.Duration) *http.Client
	CacheDir       string
	Logger         *log.Logger
	OnQuit         func()
	EnableApply    bool

	// test hooks; left nil in production.
	detectAppFn func() (string, bool)
	installFn   func(ctx context.Context, zipPath, stageDir, targetApp string, onQuit func(), logger *log.Logger) error
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type githubRelease struct {
	TagName    string         `json:"tag_name"`
	HTMLURL    string         `json:"html_url"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

// Manager coordinates periodic update checks and background self-updates.
type Manager struct {
	repo          string
	currentRaw    string
	currentVer    semver
	currentValid  bool
	apiBaseURL    string
	checkInterval time.Duration
	initialDelay  time.Duration
	clientFactory func(timeout time.Duration) *http.Client
	cacheDir      string
	log           *log.Logger
	onQuit        func()
	enableApply   bool
	targetApp     string
	selfUpdatable bool
	installFn     func(ctx context.Context, zipPath, stageDir, targetApp string, onQuit func(), logger *log.Logger) error

	mu          sync.Mutex
	started     bool
	status      Status
	etag        string
	lastCheck   time.Time
	assetName   string
	assetURL    string
	assetSize   int64
	checksumURL string
}

// New creates a Manager with sensible defaults.
func New(cfg Config) *Manager {
	repo := cfg.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	apiBase := strings.TrimRight(cfg.APIBaseURL, "/")
	if apiBase == "" {
		apiBase = DefaultAPIBaseURL
	}
	interval := cfg.CheckInterval
	if interval <= 0 {
		interval = DefaultCheckInterval
	}
	delay := cfg.InitialDelay
	if delay < 0 {
		delay = 0
	}
	clientFactory := cfg.ClientFactory
	if clientFactory == nil {
		clientFactory = func(timeout time.Duration) *http.Client {
			return &http.Client{Timeout: timeout}
		}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		if base, err := os.UserCacheDir(); err == nil {
			cacheDir = filepath.Join(base, "Gauge", "updates")
		} else {
			cacheDir = filepath.Join(os.TempDir(), "gauge-updates")
		}
	}

	rawVer := strings.TrimSpace(cfg.CurrentVersion)
	if override := strings.TrimSpace(os.Getenv("GAUGE_UPDATE_CURRENT_VERSION")); override != "" {
		rawVer = override
	}
	ver, valid := parseSemver(rawVer)
	displayVer := rawVer
	if valid {
		displayVer = ver.String()
	}

	detect := cfg.detectAppFn
	if detect == nil {
		detect = detectSelfAppBundle
	}
	install := cfg.installFn
	if install == nil {
		install = installAndRestart
	}

	var targetApp string
	var selfUpdatable bool
	if cfg.EnableApply {
		targetApp, selfUpdatable = detect()
	}

	return &Manager{
		repo:          repo,
		currentRaw:    rawVer,
		currentVer:    ver,
		currentValid:  valid,
		apiBaseURL:    apiBase,
		checkInterval: interval,
		initialDelay:  delay,
		clientFactory: clientFactory,
		cacheDir:      cacheDir,
		log:           logger,
		onQuit:        cfg.OnQuit,
		enableApply:   cfg.EnableApply,
		targetApp:     targetApp,
		selfUpdatable: selfUpdatable,
		installFn:     install,
		status: Status{
			Current: displayVer,
			State:   StateIdle,
		},
	}
}

// Start launches the periodic background update checker. Calling Start more
// than once is a no-op. Dev builds without a valid semver version skip
// automatic checks unless GAUGE_UPDATE_CURRENT_VERSION is set.
func (m *Manager) Start(ctx context.Context) {
	if !m.currentValid {
		return
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()

	go func() {
		if m.initialDelay > 0 {
			timer := time.NewTimer(m.initialDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		m.Check(ctx)

		ticker := time.NewTicker(m.checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.Check(ctx)
			}
		}
	}()
}

// Status returns a copy of the current update state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Check queries GitHub Releases for the latest release and updates the state.
// Network and rate-limit failures are logged quietly without surfacing an error
// banner on the home page.
func (m *Manager) Check(ctx context.Context) Status {
	if !m.currentValid {
		return m.Status()
	}

	m.mu.Lock()
	switch m.status.State {
	case StateChecking, StateDownloading, StateInstalling:
		st := m.status
		m.mu.Unlock()
		return st
	}
	prevState := m.status.State
	etag := m.etag
	m.status.State = StateChecking
	m.mu.Unlock()

	rel, newETag, notModified, err := m.fetchLatestRelease(ctx, etag)

	m.mu.Lock()
	defer m.mu.Unlock()

	// If Apply() somehow raced or state changed unexpectedly, leave it alone.
	if m.status.State != StateChecking {
		return m.status
	}

	if err != nil {
		m.log.Printf("updater: check failed: %v", err)
		m.status.State = prevState
		return m.status
	}

	m.lastCheck = time.Now()
	if notModified {
		m.status.State = prevState
		return m.status
	}
	if newETag != "" {
		m.etag = newETag
	}
	if rel.Draft || rel.Prerelease {
		m.status.State = prevState
		return m.status
	}

	latestVer, ok := parseSemver(rel.TagName)
	if !ok {
		m.log.Printf("updater: ignoring unparseable release tag %q", rel.TagName)
		m.status.State = prevState
		return m.status
	}

	if compareSemver(latestVer, m.currentVer) <= 0 {
		m.status.Latest = latestVer.String()
		m.status.Available = false
		m.status.CanApply = false
		m.status.ReleaseURL = rel.HTMLURL
		m.status.Error = ""
		m.status.State = StateIdle
		return m.status
	}

	wantZip := fmt.Sprintf("Gauge-%s-macos-universal.zip", latestVer.String())
	var zipAsset *releaseAsset
	var checksumURL string
	for i := range rel.Assets {
		a := &rel.Assets[i]
		switch a.Name {
		case wantZip:
			zipAsset = a
		case "SHA256SUMS.txt":
			checksumURL = a.BrowserDownloadURL
		}
	}

	m.status.Latest = latestVer.String()
	m.status.Available = true
	m.status.ReleaseURL = rel.HTMLURL
	m.status.Error = ""
	m.status.Progress = 0
	m.status.State = StateAvailable

	if zipAsset != nil && checksumURL != "" {
		m.assetName = zipAsset.Name
		m.assetURL = zipAsset.BrowserDownloadURL
		m.assetSize = zipAsset.Size
		m.checksumURL = checksumURL
		m.status.CanApply = m.selfUpdatable
	} else {
		m.assetName = ""
		m.assetURL = ""
		m.assetSize = 0
		m.checksumURL = ""
		m.status.CanApply = false
	}

	m.log.Printf("updater: new version %s available (current %s, can_apply=%v)", latestVer.String(), m.status.Current, m.status.CanApply)
	return m.status
}

// CheckManual runs Check unless a recent check already succeeded within the
// cooldown window.
func (m *Manager) CheckManual(ctx context.Context) Status {
	m.mu.Lock()
	recent := !m.lastCheck.IsZero() && time.Since(m.lastCheck) < manualCheckCooldown
	st := m.status
	m.mu.Unlock()
	if recent {
		return st
	}
	return m.Check(ctx)
}

// Apply starts downloading, verifying, and installing the available update in
// the background. It returns immediately once the job is accepted.
func (m *Manager) Apply() error {
	m.mu.Lock()
	if m.status.State == StateDownloading || m.status.State == StateInstalling {
		m.mu.Unlock()
		return nil
	}
	if !m.status.Available {
		m.mu.Unlock()
		return errors.New("no update available")
	}
	if !m.status.CanApply || m.assetURL == "" || m.checksumURL == "" {
		m.mu.Unlock()
		return errors.New("automatic install is unavailable for this installation")
	}
	ver := m.status.Latest
	assetName := m.assetName
	assetURL := m.assetURL
	assetSize := m.assetSize
	checksumURL := m.checksumURL
	targetApp := m.targetApp

	m.status.State = StateDownloading
	m.status.Progress = 0
	m.status.Error = ""
	m.mu.Unlock()

	go m.runApply(ver, assetName, assetURL, assetSize, checksumURL, targetApp)
	return nil
}

func (m *Manager) runApply(ver, assetName, assetURL string, assetSize int64, checksumURL, targetApp string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	fail := func(err error) {
		m.log.Printf("updater: apply %s failed: %v", ver, err)
		m.mu.Lock()
		m.status.State = StateError
		m.status.Progress = 0
		m.status.Error = err.Error()
		m.mu.Unlock()
	}

	stageDir := filepath.Join(m.cacheDir, ver)
	_ = os.RemoveAll(stageDir)
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		fail(fmt.Errorf("create staging dir: %w", err))
		return
	}

	// 1. Download SHA256SUMS.txt first so we know the expected hash before
	// finishing the larger archive download.
	expectedHash, err := m.fetchExpectedSHA256(ctx, checksumURL, assetName)
	if err != nil {
		fail(fmt.Errorf("fetch checksum: %w", err))
		return
	}

	// 2. Stream the release zip to disk while computing SHA-256 and progress.
	zipPath := filepath.Join(stageDir, assetName)
	gotHash, err := m.downloadFile(ctx, assetURL, zipPath, assetSize)
	if err != nil {
		fail(fmt.Errorf("download archive: %w", err))
		return
	}
	if !strings.EqualFold(gotHash, expectedHash) {
		_ = os.Remove(zipPath)
		fail(fmt.Errorf("checksum mismatch for %s", assetName))
		return
	}

	// 3. Hand off extraction, signature verification, atomic swap, and silent restart.
	m.mu.Lock()
	m.status.State = StateInstalling
	m.status.Progress = 100
	m.mu.Unlock()

	if err := m.installFn(ctx, zipPath, stageDir, targetApp, m.onQuit, m.log); err != nil {
		fail(err)
		return
	}
}

func (m *Manager) fetchLatestRelease(ctx context.Context, etag string) (*githubRelease, string, bool, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", m.apiBaseURL, m.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Gauge/"+m.currentRaw)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := m.clientFactory(15 * time.Second).Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("github api returned %s", resp.Status)
	}

	var rel githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&rel); err != nil {
		return nil, "", false, err
	}
	return &rel, resp.Header.Get("ETag"), false, nil
}

func (m *Manager) fetchExpectedSHA256(ctx context.Context, checksumURL, assetName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Gauge/"+m.currentRaw)

	resp, err := m.clientFactory(30 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum download returned %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksumBytes))
	if err != nil {
		return "", err
	}
	return parseSHA256Sums(string(raw), assetName)
}

func (m *Manager) downloadFile(ctx context.Context, url, destPath string, expectedSize int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Gauge/"+m.currentRaw)

	resp, err := m.clientFactory(5 * time.Minute).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %s", resp.Status)
	}

	total := resp.ContentLength
	if total <= 0 {
		total = expectedSize
	}

	partPath := destPath + ".part"
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}

	hasher := sha256.New()
	var written int64
	buf := make([]byte, 32*1024)
	lastPct := -1

	reader := io.LimitReader(resp.Body, maxAssetBytes)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				_ = os.Remove(partPath)
				return "", werr
			}
			_, _ = hasher.Write(buf[:n])
			written += int64(n)
			if total > 0 {
				pct := int(written * 100 / total)
				if pct > 99 {
					pct = 99
				}
				if pct != lastPct {
					lastPct = pct
					m.mu.Lock()
					if m.status.State == StateDownloading {
						m.status.Progress = pct
					}
					m.mu.Unlock()
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			_ = os.Remove(partPath)
			return "", readErr
		}
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(partPath)
		return "", err
	}
	if err := os.Rename(partPath, destPath); err != nil {
		_ = os.Remove(partPath)
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// parseSHA256Sums extracts the hex SHA-256 digest for targetName from a
// standard `sha256sum` output file.
func parseSHA256Sums(content, targetName string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		hash := strings.TrimSpace(fields[0])
		name := strings.TrimPrefix(strings.TrimSpace(fields[1]), "*")
		name = filepath.Base(name)
		if name != targetName {
			continue
		}
		if len(hash) != 64 {
			return "", fmt.Errorf("invalid sha256 length for %s", targetName)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return "", fmt.Errorf("invalid sha256 hex for %s", targetName)
		}
		return strings.ToLower(hash), nil
	}
	return "", fmt.Errorf("%s not found in SHA256SUMS.txt", targetName)
}

type semver struct {
	major int
	minor int
	patch int
}

func (v semver) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

// parseSemver extracts MAJOR.MINOR.PATCH from strings like "v0.3.1", "0.3.1",
// or "v0.3.1-3-gabc1234-dirty". Returns false for "dev" or commit hashes.
func parseSemver(raw string) (semver, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return semver{}, false
	}
	// Drop any prerelease/build suffix (e.g. -3-gabc1234-dirty or +build).
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return semver{}, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return semver{}, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return semver{}, false
	}
	patch := 0
	if len(parts) == 3 {
		patch, err = strconv.Atoi(parts[2])
		if err != nil || patch < 0 {
			return semver{}, false
		}
	}
	return semver{major: major, minor: minor, patch: patch}, true
}

func compareSemver(a, b semver) int {
	if a.major != b.major {
		if a.major < b.major {
			return -1
		}
		return 1
	}
	if a.minor != b.minor {
		if a.minor < b.minor {
			return -1
		}
		return 1
	}
	if a.patch != b.patch {
		if a.patch < b.patch {
			return -1
		}
		return 1
	}
	return 0
}
