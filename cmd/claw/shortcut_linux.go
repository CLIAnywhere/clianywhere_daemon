//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// userDesktopPath resolves the user's Desktop directory, honoring XDG.
//  1. $XDG_DESKTOP_DIR if set and absolute
//  2. ~/.config/user-dirs.dirs "XDG_DESKTOP_DIR" entry (covers localized
//     desktop directory names, e.g. non-English locales)
//  3. $HOME/Desktop as a final fallback
//
// Returns an error only if $HOME itself is unreadable.
func userDesktopPath() (string, error) {
	// 1. environment variable
	if d := os.Getenv("XDG_DESKTOP_DIR"); d != "" && filepath.IsAbs(d) {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d, nil
		}
	}

	// 2. parse user-dirs.dirs
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	cfgPath := filepath.Join(home, ".config", "user-dirs.dirs")
	if f, err := os.Open(cfgPath); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "XDG_DESKTOP_DIR=") {
				continue
			}
			val := strings.TrimPrefix(line, "XDG_DESKTOP_DIR=")
			val = strings.Trim(val, `"`)
			// Expand $HOME-relative references like "$HOME/Desktop"
			if strings.HasPrefix(val, "$HOME") {
				val = filepath.Join(home, strings.TrimPrefix(val, "$HOME"))
			} else if !filepath.IsAbs(val) {
				continue // unexpected format, skip
			}
			if fi, err := os.Stat(val); err == nil && fi.IsDir() {
				return val, nil
			}
		}
	}

	// 3. fallback
	desktop := filepath.Join(home, "Desktop")
	if fi, err := os.Stat(desktop); err == nil && fi.IsDir() {
		return desktop, nil
	}
	return "", fmt.Errorf("desktop directory not found")
}

// createDesktopShortcut writes a freedesktop.org .desktop entry on the user's
// desktop pointing at the running executable. Marks it executable so most
// desktop environments will run it on double-click (GNOME 43+ additionally
// requires a one-time "Allow launching" via right-click, which we cannot bypass).
func createDesktopShortcut() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve exe: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = real
	}
	workDir := filepath.Dir(exePath)

	desktop, err := userDesktopPath()
	if err != nil {
		return fmt.Errorf("resolve desktop: %w", err)
	}

	// 固定文件名为 CLIAnywhere.desktop，与展示名 Name= 保持一致，
	// 这样文件管理器和 Dock 里显示的就是 CLIAnywhere 而不是可执行文件名。
	desktopPath := filepath.Join(desktop, "CLIAnywhere.desktop")

	// .desktop entry — Type=Application so it's launchable from the file manager.
	// %f is a freedesktop placeholder (file argument); we don't consume it but
	// including it follows the spec and silences some validators.
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=CLIAnywhere
Comment=CLIAnywhere local web terminal
Exec=%s
Path=%s
Icon=utilities-terminal
Terminal=false
Categories=Development;System;
`,
		exePath, workDir)

	if err := os.WriteFile(desktopPath, []byte(content), 0o755); err != nil {
		return fmt.Errorf("write .desktop: %w", err)
	}
	_ = os.Chmod(desktopPath, 0o755)
	// Mark the file as trusted for GNOME — disables the "Untrusted application
	// launcher" prompt. Best-effort: only works on Nautilus-based DEs.
	_ = markDesktopTrusted(desktopPath)
	return nil
}
