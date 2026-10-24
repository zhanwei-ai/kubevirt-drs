// Package version holds the build version, set with -ldflags "-X .../internal/version.Version=v0.1.0".
package version

// Version is "dev" unless overridden at build time.
var Version = "dev"
