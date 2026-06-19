package pgdesk

import "embed"

// templatesFS holds the server-rendered HTML templates, compiled once at New()
// (F5). Using embed.FS structurally prevents path traversal (F8).
//
//go:embed templates/*.html
var templatesFS embed.FS

// assetsFS holds static CSS/JS served under <basePath>/_static/ with long
// immutable cache headers on fingerprinted paths (F8). No directory listing, no
// external CDN.
//
//go:embed assets/*
var assetsFS embed.FS
