// Package xterm is a pure-Go headless terminal emulator vendored from xterm-go.
//
// This is an in-tree copy of github.com/gitpod-io/xterm-go (MIT license,
// Copyright (c) 2026 Ona), which is itself a Go port of the headless subset of
// https://github.com/xtermjs/xterm.js (MIT license). The upstream LICENSE is
// preserved alongside this file at ./LICENSE.
//
// It processes VT/ANSI escape sequences and maintains terminal buffer state
// without requiring a browser, DOM, or any rendering. This enables server-side
// terminal state tracking, screen content extraction, and headless terminal testing.
//
// The implementation follows the VT500 specification.
package xterm
