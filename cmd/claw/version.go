package main

// Version is the daemon release version. Single source of truth — referenced by
// the "version" subcommand and reported to the TurnServer on login so the
// globalserver can track per-device daemon versions.
//
// Bump this manually for each release. Init: 1.0.1.
var Version = "1.0.8"
