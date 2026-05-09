//go:build !windows

package main

import (
	"fmt"

	qrcode "github.com/skip2/go-qrcode"
)

// showQRImage is only used on Windows; no-op stub for Unix
func showQRImage(qr *qrcode.QRCode) error { return fmt.Errorf("not implemented") }
