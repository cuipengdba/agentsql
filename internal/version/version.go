// Package version exposes the build version shared by AgentSQL binaries.
package version

// Version is overridden in release builds through -ldflags -X.
var Version = "v0.2.0"
