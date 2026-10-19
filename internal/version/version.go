// Package version exposes the build version shared by AgentSQL binaries.
package version

// Version is overridden in release builds through -ldflags -X.
var Version = "v2.4.0"
