// Package web embeds the dashboard shell so the binary stays self-contained.
package web

import "embed"

// FS holds the static assets served at /. Every file the pages load must be
// listed; embed_test.go fails when one is missing.
//
//go:embed index.html settings.html shell.html popover.html app.css desktop.css popover.css settings-ui.css app.js settings.js popover.js settings-ui.js mark.svg mark-classic.svg icons.js icon-opendesign.png
var FS embed.FS
