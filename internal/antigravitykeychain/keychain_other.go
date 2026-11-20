//go:build !darwin

package antigravitykeychain

import "context"

// Supported reports whether this platform has the shared item.
func Supported() bool { return false }

// Raw is unavailable outside macOS.
func Raw(context.Context) ([]byte, error) { return nil, ErrUnsupported }

// Read is unavailable outside macOS.
func Read(context.Context) (Credential, error) { return Credential{}, ErrUnsupported }

// Write is unavailable outside macOS.
func Write(context.Context, []byte) error { return ErrUnsupported }
