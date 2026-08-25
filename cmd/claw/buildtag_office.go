//go:build office

package main

// isOfficeBuild is true only for binaries built by the official CI
// (go build -tags "… office"). Self-built binaries (plain `go build`)
// do not define the office tag: they may CHECK for updates but must not
// apply prebuilt release packages — their update path is fetching the
// latest source and rebuilding.
const isOfficeBuild = true
