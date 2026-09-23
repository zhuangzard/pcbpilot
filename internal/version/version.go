package version

// Version is overridden at build time via -ldflags for release builds.
var (
	Name    = "pcbpilot"
	Version = "dev"
)
