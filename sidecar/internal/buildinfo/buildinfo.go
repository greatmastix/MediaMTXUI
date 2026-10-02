// Package buildinfo holds values stamped into the binary at link time.
package buildinfo

// Set by the image build with
// -ldflags "-X mtxui/internal/buildinfo.Version=... -X mtxui/internal/buildinfo.MediaMTXVersion=...".
var (
	// Version is the sidecar's own version (git describe, or "dev").
	Version = "dev"
	// MediaMTXVersion is the MediaMTX release whose API this binary was built against (deploy/tools.env).
	MediaMTXVersion = "unknown"
)
