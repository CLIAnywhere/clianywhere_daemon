//go:build cli

package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	qrcode "github.com/skip2/go-qrcode"
)

// GetAccessKey CLI version: obtain accesskey via console interaction
func GetAccessKey(cfg *Config, logger Logger) (string, error) {
	choice := showArrowKeyMenu("How to get your AccessKey?", []string{
		"Enter AccessKey manually",
		"Scan QR code from app",
	})

	switch choice {
	case 0:
		return inputAccessKeyCLI(bufio.NewScanner(os.Stdin)), nil
	case 1:
		return qrBindModeCLI(cfg, logger)
	default:
		return "", fmt.Errorf("cancelled")
	}
}

// inputAccessKeyCLI manually input accesskey
func inputAccessKeyCLI(scanner *bufio.Scanner) string {
	for {
		fmt.Print("Enter your AccessKey: ")
		if !scanner.Scan() {
			log.Fatal("Failed to read input")
		}
		key := scanner.Text()
		if accessKeyRegex.MatchString(key) {
			return key
		}
		fmt.Println("Invalid AccessKey (only 0-9, a-z allowed), try again.")
	}
}

// qrBindModeCLI QR code binding mode: platform-specific UI
func qrBindModeCLI(cfg *Config, logger Logger) (string, error) {
	// Windows: use Win32 window
	if runtime.GOOS == "windows" {
		return qrBindModeUI(cfg)
	}

	// Unix/Linux/macOS: terminal QR rendering + CLI confirmation
	return qrBindModeTerminal(cfg, logger)
}

// qrBindModeTerminal terminal-based QR binding flow (Unix/Linux/macOS)
func qrBindModeTerminal(cfg *Config, logger Logger) (string, error) {
	// 1. generate bindcode
	result, err := GenerateBindCode(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to generate bindcode: %w", err)
	}

	// 2. check terminal width (2 chars per module)
	if termWidth, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && termWidth < 80 {
		return "", fmt.Errorf("terminal too narrow (%d cols), need 80+ cols", termWidth)
	}

	// 3. generate and print QR code
	qr, err := qrcode.New(string(result.QRPayload), qrcode.Low)
	if err != nil {
		return "", fmt.Errorf("failed to generate QR code: %w", err)
	}
	qr.DisableBorder = true

	fmt.Println()
	fmt.Println("Scan this QR code with the CliAnyWhere app:")
	fmt.Println(renderQRHalfBlock(qr.Bitmap()))
	fmt.Printf("Waiting for binding (bindcode: %s...)...\n", result.BindCode[:8])
	fmt.Println()

	// 4. connect to TS and send bindcode
	conn, err := ConnectAndSendBindCode(result.TSWSURL, result.BindCode)
	if err != nil {
		return "", fmt.Errorf("failed to connect for bindcode: %w", err)
	}
	defer conn.Close()

	// 5. wait for bind response
	accesskey, deviceName, err := WaitForBindResponse(conn, 120*time.Second)
	if err != nil {
		return "", fmt.Errorf("binding failed: %w", err)
	}

	// 6. security confirmation (arrow key menu)
	confirm := showArrowKeyMenu(
		fmt.Sprintf("Bind AccessKey from \"%s\"?", deviceName),
		[]string{
			"Yes, bind this AccessKey",
			"No, cancel",
		},
	)
	if confirm != 0 {
		return "", fmt.Errorf("binding cancelled")
	}
	return accesskey, nil
}

// truncateKey truncate accesskey for display (show first 8 chars + ...)
func truncateKey(key string) string {
	if len(key) <= 8 {
		return key
	}
	return key[:8] + "..."
}

// renderQRBackground renders QR code using ANSI background colors (for Windows CMD fallback)
// Each module is 2 spaces wide × 1 line tall. Background color fills the cell completely,
// avoiding the line-spacing gaps that break character-based rendering on Windows.
func renderQRBackground(bitmap [][]bool) string {
	size := len(bitmap)
	var sb strings.Builder
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if bitmap[y][x] {
				sb.WriteString("\x1b[40m  ") // dark module: black background
			} else {
				sb.WriteString("\x1b[47m  ") // light module: white background
			}
		}
		sb.WriteString("\x1b[0m\n") // reset after each line
	}
	return sb.String()
}

// renderQRHalfBlock renders QR code using Unicode half-block characters
// every 2 rows of QR modules merge into 1 terminal output line, area reduced to 1/4, maintaining correct aspect ratio
// ▀ = top dark, bottom light  ▄ = top light, bottom dark  █ = all dark  space = all light
func renderQRHalfBlock(bitmap [][]bool) string {
	size := len(bitmap)
	var sb strings.Builder
	for y := 0; y < size; y += 2 {
		for x := 0; x < size; x++ {
			top := bitmap[y][x]
			bottom := false
			if y+1 < size {
				bottom = bitmap[y+1][x]
			}
			switch {
			case top && bottom:
				sb.WriteString("█")
			case top && !bottom:
				sb.WriteString("▀")
			case !top && bottom:
				sb.WriteString("▄")
			default:
				sb.WriteString(" ")
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
