//go:build linux

package checkupdate

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ApplyUpdate downloads the zip, extracts the new binary and atomically
// replaces our own executable. The daemon then exits (see exitAfterUpdate
// in cmd/claw) and the user restarts the server.
func ApplyUpdate(res *Result) error {
	asset := PlatformAsset(res.Release)
	if asset == nil {
		return fmt.Errorf("checkupdate: no linux asset in release %s", res.Release.Name)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("checkupdate: locate process: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	// The zip lands in the process dir; extract next to the exe so the
	// final rename stays on the same filesystem (rename across devices fails).
	zipPath, err := Download(asset)
	if err != nil {
		return err
	}
	defer os.Remove(zipPath)

	newExe := exe + ".new"
	if err := extractBinary(zipPath, "claw", newExe); err != nil {
		os.Remove(newExe)
		return err
	}

	// Atomic self-replace: rename over the running binary's path. The
	// running process keeps its old inode (writing the file in place would
	// fail with ETXTBSY); the new binary takes effect on next launch.
	logger.Infof("[UPDATE] replacing binary: %s", exe)
	if err := os.Rename(newExe, exe); err != nil {
		os.Remove(newExe)
		return fmt.Errorf("checkupdate: replace binary: %w", err)
	}
	return nil
}

// extractBinary pulls the entry named binName out of the zip and writes it
// to dest with executable permissions.
func extractBinary(zipPath, binName, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("checkupdate: open zip %s: %w", zipPath, err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		if f.FileInfo().IsDir() || filepath.Base(f.Name) != binName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("checkupdate: read zip entry %s: %w", f.Name, err)
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return fmt.Errorf("checkupdate: create %s: %w", dest, err)
		}
		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			return fmt.Errorf("checkupdate: extract %s: %w", f.Name, err)
		}
		rc.Close()
		if err := out.Close(); err != nil {
			return fmt.Errorf("checkupdate: extract %s: %w", f.Name, err)
		}
		return nil
	}
	return fmt.Errorf("checkupdate: no %s binary inside %s", binName, zipPath)
}
