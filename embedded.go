package webring

import (
	"embed"
)

//go:embed static internal/dashboard/templates internal/public/templates
var Files embed.FS
