//go:build web && darwin

package main

// macOS builds do not embed the web app zip. It ships as a standalone file in
// CLIAnywhere.app/Contents/Resources (copied there by the build script) and is
// loaded at runtime by readExternalWebappZip, so a universal binary carries a
// single copy instead of one per architecture.
var webappZip []byte
