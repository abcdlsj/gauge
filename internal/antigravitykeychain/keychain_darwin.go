//go:build darwin

package antigravitykeychain

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// The item Antigravity IDE and the agy CLI both read.
const (
	service = "gemini"
	account = "antigravity"
	// go-keyring prefixes values it wrote itself; the clients decode the
	// envelope, so every writer has to produce it.
	keyringPrefix = "go-keyring-base64:"
)

// Supported reports whether this platform has the shared item.
func Supported() bool { return true }

// command runs a short-lived security(1) call and returns its output.
func command(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "security", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", errors.New(detail)
	}
	return string(out), nil
}

// Raw reads the item verbatim and strips the go-keyring envelope. The bytes are
// returned alongside so a caller can restore exactly what it replaced.
func Raw(ctx context.Context) ([]byte, error) {
	out, err := command(ctx, "find-generic-password", "-s", service, "-a", account, "-w")
	if err != nil {
		return nil, err
	}
	value := strings.TrimRight(out, "\r\n")
	if value == "" {
		return nil, errors.New("the Antigravity Keychain item is empty")
	}
	if strings.HasPrefix(value, keyringPrefix) {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, keyringPrefix))
		if err != nil {
			return nil, fmt.Errorf("decode the Keychain item: %w", err)
		}
		return decoded, nil
	}
	return []byte(value), nil
}

// Read returns the current sign-in.
func Read(ctx context.Context) (Credential, error) {
	raw, err := Raw(ctx)
	if err != nil {
		return Credential{}, err
	}
	cred, err := Parse(raw)
	if err != nil {
		return Credential{}, fmt.Errorf("parse the Keychain item: %w", err)
	}
	return cred, nil
}

// Write replaces the item. Deleting first keeps the write deterministic:
// "add-generic-password" fails when the item already exists.
func Write(ctx context.Context, raw []byte) error {
	sealed := keyringPrefix + base64.StdEncoding.EncodeToString(raw)
	// A missing item makes delete fail; that is the normal fresh-install path.
	_, _ = command(ctx, "delete-generic-password", "-s", service, "-a", account)
	if _, err := command(ctx, "add-generic-password", "-s", service, "-a", account,
		"-w", sealed, "-A"); err != nil {
		return err
	}
	// Read back so a silent truncation cannot leave the clients signed out.
	written, err := Raw(ctx)
	if err != nil {
		return fmt.Errorf("confirm the Keychain write: %w", err)
	}
	if string(written) != string(raw) {
		return errors.New("the Keychain item does not match what was written")
	}
	return nil
}
