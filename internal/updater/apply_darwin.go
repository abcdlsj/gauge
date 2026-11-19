//go:build darwin

package updater

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// detectSelfAppBundle resolves the enclosing .app bundle when running from a
// packaged macOS application and checks that its parent directory is writable.
func detectSelfAppBundle() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	return inspectAppBundlePath(exe)
}

// inspectAppBundlePath is separated from os.Executable for unit testing.
func inspectAppBundlePath(exePath string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		resolved = exePath
	}
	// Expected layout: <dir>/<Name>.app/Contents/MacOS/<binary>
	macosDir := filepath.Dir(resolved)
	contentsDir := filepath.Dir(macosDir)
	appDir := filepath.Dir(contentsDir)
	if filepath.Base(macosDir) != "MacOS" || filepath.Base(contentsDir) != "Contents" || !strings.HasSuffix(appDir, ".app") {
		return "", false
	}
	if strings.HasPrefix(appDir, "/Volumes/") || strings.Contains(appDir, "AppTranslocation") {
		return "", false
	}
	parent := filepath.Dir(appDir)
	probe, err := os.CreateTemp(parent, ".gauge-update-probe-*")
	if err != nil {
		return "", false
	}
	probePath := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probePath)
	return appDir, true
}

const applyScript = `#!/bin/sh
set -eu

OLD_PID="$1"
TARGET_APP="$2"
STAGED_APP="$3"
STAGE_DIR="$4"

# Wait for the running Gauge process to exit cleanly (flushes SQLite WAL).
i=0
while kill -0 "$OLD_PID" 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 75 ]; then
    exit 1
  fi
  sleep 0.2
done

PARENT_DIR=$(dirname "$TARGET_APP")
APP_NAME=$(basename "$TARGET_APP")
NEW_TMP="$PARENT_DIR/.${APP_NAME}.new.$$"
OLD_TMP="$PARENT_DIR/.${APP_NAME}.old.$$"

cleanup_new() {
  rm -rf "$NEW_TMP"
}

# Stage onto the same directory/volume first so the swap is two atomic renames.
/usr/bin/ditto "$STAGED_APP" "$NEW_TMP"

if ! mv "$TARGET_APP" "$OLD_TMP"; then
  cleanup_new
  exit 1
fi

if ! mv "$NEW_TMP" "$TARGET_APP"; then
  mv "$OLD_TMP" "$TARGET_APP" || true
  cleanup_new
  exit 1
fi

rm -rf "$OLD_TMP" "$STAGE_DIR" || true

# Silent relaunch: Gauge is an Accessory menubar app, so it returns to the menu
# bar without a Dock bounce or popup window.
/usr/bin/open "$TARGET_APP"
`

// installAndRestart extracts and verifies the downloaded zip, launches a
// detached helper that atomically replaces targetApp once this process exits,
// and triggers graceful shutdown via onQuit.
func installAndRestart(ctx context.Context, zipPath, stageDir, targetApp string, onQuit func(), logger *log.Logger) error {
	if targetApp == "" {
		return errors.New("target app bundle path is empty")
	}

	extractDir := filepath.Join(stageDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o700); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}

	// ditto matches `ditto -c -k --keepParent` in Makefile and preserves
	// permissions, symlinks, and bundle metadata.
	if out, err := exec.CommandContext(ctx, "/usr/bin/ditto", "-x", "-k", zipPath, extractDir).CombinedOutput(); err != nil {
		return fmt.Errorf("extract archive: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	stagedApp := filepath.Join(extractDir, "Gauge.app")
	binPath := filepath.Join(stagedApp, "Contents", "MacOS", "Gauge")
	info, err := os.Stat(binPath)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return errors.New("extracted bundle is missing executable Gauge binary")
	}

	// Verify the ad-hoc (or Developer ID) signature wasn't corrupted.
	if out, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", stagedApp).CombinedOutput(); err != nil {
		return fmt.Errorf("verify bundle signature: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	scriptPath := filepath.Join(stageDir, "apply.sh")
	if err := os.WriteFile(scriptPath, []byte(applyScript), 0o700); err != nil {
		return fmt.Errorf("write updater helper: %w", err)
	}

	cmd := exec.Command("/bin/sh", scriptPath, strconv.Itoa(os.Getpid()), targetApp, stagedApp, stageDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start updater helper: %w", err)
	}
	_ = cmd.Process.Release()

	logger.Printf("updater: helper launched, quitting for silent restart into %s", targetApp)
	if onQuit != nil {
		onQuit()
	} else {
		os.Exit(0)
	}
	return nil
}
