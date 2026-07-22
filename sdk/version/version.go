// Package version exposes the MOW release version to all first-party modules.
// VERSION at the repository root is authoritative; release builds override this
// variable with -ldflags "-X github.com/mow/mow/sdk/version.Version=<version>".
package version

// Version is the current source-tree version. Release automation verifies it
// against the Git tag before building artifacts.
var Version = "0.5.4"
