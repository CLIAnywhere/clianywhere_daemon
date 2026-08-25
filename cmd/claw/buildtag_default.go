//go:build !office

package main

// isOfficeBuild is false for user self-built binaries — see
// buildtag_office.go. They can check for updates but applying a prebuilt
// release package is refused; the user updates by re-fetching the source
// and rebuilding.
const isOfficeBuild = false
