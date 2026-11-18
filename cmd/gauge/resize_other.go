//go:build !darwin || !cgo || purego

package main

import "github.com/wailsapp/wails/v3/pkg/application"

func resizePopover(w *application.WebviewWindow, width, height int) {
	w.SetSize(width, height)
}
