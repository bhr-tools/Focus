package web

import "embed"

// Files contains all embedded static assets for the web frontend.
//
//go:embed *
var Files embed.FS
