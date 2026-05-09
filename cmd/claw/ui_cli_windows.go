//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	qrcode "github.com/skip2/go-qrcode"
)

// showQRImage saves QR code as PNG and opens with Windows default image viewer
func showQRImage(qr *qrcode.QRCode) error {
	// generate PNG bytes
	png, err := qr.PNG(256)
	if err != nil {
		return fmt.Errorf("failed to generate PNG: %w", err)
	}

	// save to temp file
	tmpDir := os.TempDir()
	path := filepath.Join(tmpDir, "clianywhere_qr.png")
	if err := os.WriteFile(path, png, 0644); err != nil {
		return fmt.Errorf("failed to write PNG: %w", err)
	}

	fmt.Printf("QR code image opened: %s\n", path)
	fmt.Println("(Close the image after scanning)")

	// open with Windows default viewer
	cmd := exec.Command("cmd", "/c", "start", "", path)
	return cmd.Start()
}
