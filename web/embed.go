// Package web embeds the dashboard shell so the binary stays self-contained.
package web

import "embed"

// FS holds the static assets served at /.
//
//go:embed index.html settings.html shell.html popover.html app.css desktop.css popover.css app.js settings.js popover.js mark.svg
var FS embed.FS
