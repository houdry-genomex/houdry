package version

// Version is the default semantic version for this tree.
// Release builds override it via:
//
//	go build -ldflags "-X houdry/internal/version.Version=0.6.1"
//
// (see Makefile VERSION / .github/workflows/release.yml).
var Version = "0.6.3"
