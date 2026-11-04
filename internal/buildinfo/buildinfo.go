// Package buildinfo says which build a binary is: its release version, the
// commit it was built from, and when.
//
// Release builds stamp all three at link time (build/build.sh does this):
//
//	-ldflags "-X github.com/rahulguptaio/timlee/internal/buildinfo.Version=1.4.0
//	          -X github.com/rahulguptaio/timlee/internal/buildinfo.Commit=<full sha>
//	          -X github.com/rahulguptaio/timlee/internal/buildinfo.Date=2026-10-29T04:42:37Z"
//
// Anything left unstamped, such as a plain go build or go run from a checkout,
// falls back to the VCS state the Go toolchain records in the binary, so even
// a developer build can name its commit and say whether the tree was clean.
package buildinfo

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
)

// Set with -ldflags -X at link time; empty means unstamped. Date is RFC 3339.
var (
	Version string
	Commit  string
	Date    string
)

// Info is the resolved identity of the running binary.
type Info struct {
	Version  string // release version, the module pseudo-version, or "dev"
	Commit   string // full VCS revision, or "unknown"
	Date     string // RFC 3339 build date (the commit time if unstamped), or "unknown"
	Modified bool   // built from a working tree with uncommitted changes
	Go       string // toolchain, e.g. go1.27.3
	Platform string // GOOS/GOARCH
}

// Read resolves the stamp: values set by the linker first, then the VCS
// revision, commit time and modified flag recorded by the toolchain.
func Read() Info {
	bi, _ := debug.ReadBuildInfo()
	return resolve(bi)
}

func resolve(bi *debug.BuildInfo) Info {
	info := Info{
		Version:  Version,
		Commit:   Commit,
		Date:     Date,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
	if bi != nil {
		vcs := map[string]string{}
		for _, s := range bi.Settings {
			vcs[s.Key] = s.Value
		}
		if info.Commit == "" {
			info.Commit = vcs["vcs.revision"]
		}
		if info.Date == "" {
			info.Date = vcs["vcs.time"]
		}
		info.Modified = vcs["vcs.modified"] == "true"
		// Since Go 1.24 a build from a checkout carries a pseudo-version
		// derived from the commit; "(devel)" means there was nothing to derive.
		if info.Version == "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if info.Date == "" {
		info.Date = "unknown"
	}
	return info
}

// String renders the stamp on one line:
//
//	1.4.0 (commit 7896b1a…, built 2026-10-29T04:42:37Z, go1.27.3 linux/amd64)
//
// with ", modified" after the commit when the tree was dirty.
func (i Info) String() string {
	commit := i.Commit
	if i.Modified {
		commit += ", modified"
	}
	return fmt.Sprintf("%s (commit %s, built %s, %s %s)", i.Version, commit, i.Date, i.Go, i.Platform)
}

// HandleVersionFlag prints "<name> <stamp>" and exits 0 when the command line
// contains --version or -version. Call it as the first statement of main:
// before flag.Parse, which would reject the flag, and before configuration is
// loaded, so a binary can say what it is without the secrets it needs to run.
func HandleVersionFlag(name string) {
	if wantsVersion(os.Args[1:]) {
		fmt.Println(name, Read())
		os.Exit(0)
	}
}

func wantsVersion(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--":
			// The end of the flags, as for the flag package: what follows is
			// an argument that happens to look like one.
			return false
		case "--version", "-version":
			return true
		}
	}
	return false
}
