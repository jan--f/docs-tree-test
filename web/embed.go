// Package web contains the embedded, build-free browser interface.
package web

import "embed"

//go:embed index.html participant.html admin.html assets
var Assets embed.FS
