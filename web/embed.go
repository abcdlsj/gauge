// Package web embeds the menubar popover so the binary stays self-contained.
package web

import "embed"

// FS holds the static assets served at /. Every file the pages load must be
// listed; embed_test.go fails when one is missing.
//
//go:embed popover.html app.css popover.css settings-ui.css popover.js settings-ui.js mark.svg mark-classic.svg icons.js
var FS embed.FS
