package pgdesk

import "embed"

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed assets/*
var assetsFS embed.FS
