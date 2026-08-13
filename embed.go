// Package webui embeds the built frontend so the server binary is self-contained.
package webui

import "embed"

//go:embed all:web/dist
var FS embed.FS
