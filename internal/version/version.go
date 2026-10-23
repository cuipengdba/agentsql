// Package version exposes the build version shared by AgentSQL binaries.
package version

// Version is overridden in release builds through -ldflags -X.
var Version = "v3.2.0"
