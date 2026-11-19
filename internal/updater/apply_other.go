//go:build !darwin

package updater

import (
	"context"
	"errors"
	"log"
)

func detectSelfAppBundle() (string, bool) {
	return "", false
}

func installAndRestart(_ context.Context, _, _, _ string, _ func(), _ *log.Logger) error {
	return errors.New("automatic self-update is only supported for the macOS app bundle")
}
