
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

// createLogger creates CLI version Logger
func createLogger() Logger {
	return NewStdLogger()
}

// runApp CLI entry: no-arg startup (local service + accessKey handling + daemonize)
func runApp(logger Logger) {
	cfg := DefaultConfig()

	// check if another daemon instance is running (sleep to wait for parent process detach)
	time.Sleep(500 * time.Millisecond)
	if state, ok := queryDaemonStatus(); ok {
		ensureConsole()
		fmt.Printf("Daemon already running (state: %s)\n", state)
		if consoleAllocated {
			fmt.Println("Press any key to exit...")
			bufio.NewScanner(os.Stdin).Scan()
		}
		return
	}

	// read cached accessKey first — no service started yet
	newBind := false
	accessKey, err := loadAccessKey()
	if err != nil {
		fatalExit("failed to read accesskey: %v", err)
	}
	if accessKey == "" {
		// no cached key → need console for interactive binding
		ensureConsole()
		accessKey, _ = GetAccessKey(cfg, logger)
		if accessKey != "" {
			if err := saveAccessKey(accessKey); err != nil {
				fatalExit("failed to save accesskey: %v", err)
			}
			newBind = true
			releaseConsole()
		}
	}

	// binding failed or cancelled → exit cleanly, no service lingering
	if accessKey == "" {
		ensureConsole()
		fmt.Println("No AccessKey, exiting.")
		return
	}

	// key obtained → start local service and daemonize
	d := NewDaemon("", cfg, logger)
	d.Init()

	if newBind || runtime.GOOS != "windows" {
		fmt.Println("Bind successful, daemon is running in background")
	}
	if consoleAllocated {
		fmt.Println("Press any key to exit...")
		bufio.NewScanner(os.Stdin).Scan()
	}
	daemonize()
	// daemonize does not return (parent process exits)
	// if it returns, daemonization failed, continue running in foreground
	waitForSignal(d, logger)
}

// GetAccessKey CLI version: obtain accesskey via console interaction
func GetAccessKey(cfg *Config, logger Logger) (string, error) {
	fmt.Println()
	fmt.Println("How to get your AccessKey?")
	fmt.Println("  1. Enter AccessKey manually")
	fmt.Println("  2. Scan QR code from app")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("Select [1-2]: ")
		if !scanner.Scan() {
			return "", fmt.Errorf("failed to read input")
		}
		choice := scanner.Text()
		switch choice {
		case "1":
			return inputAccessKeyCLI(scanner), nil
		case "2":
			return qrBindModeCLI(cfg, scanner, logger)
		default:
			fmt.Println("Invalid choice, please enter 1 or 2.")
		}
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

// qrBindModeCLI QR code binding mode
func qrBindModeCLI(cfg *Config, scanner *bufio.Scanner, logger Logger) (string, error) {
	// 1. generate bindcode
	result, err := GenerateBindCode(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to generate bindcode: %w", err)
	}

	// 2. check terminal width (2 chars per module on all platforms)
	if termWidth, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && termWidth < 80 {
		return "", fmt.Errorf("terminal too narrow (%d cols), need 80+ cols", termWidth)
	}

	// 3. generate and print QR code
	disableQuickEditMode() // prevent CMD Quick Edit from pausing process
	qr, err := qrcode.New(string(result.QRPayload), qrcode.Low)
	if err != nil {
		return "", fmt.Errorf("failed to generate QR code: %w", err)
	}
	qr.DisableBorder = runtime.GOOS != "windows"

	fmt.Println()
	fmt.Println("Scan this QR code with the CliAnyWhere app:")
	if runtime.GOOS == "windows" {
		// Windows CMD cannot reliably render scannable QR in terminal;
		// save PNG to temp file and open with system image viewer
		if err := showQRImage(qr); err != nil {
			// fallback to terminal rendering if image viewer fails
			setConsoleUTF8()
			fmt.Println(renderQRBackground(qr.Bitmap()))
		}
	} else {
		fmt.Println(renderQRHalfBlock(qr.Bitmap()))
	}
	fmt.Printf("Waiting for binding (bindcode: %s...)...\n", result.BindCode[:8])
	fmt.Println()

	// 4. connect to TS and send bindcode
	conn, err := ConnectAndSendBindCode(result.TSWSURL, result.BindCode)
	if err != nil {
		return "", fmt.Errorf("failed to connect for bindcode: %w", err)
	}
	defer conn.Close()

	logPrintf(logger, "[bind]", "bindcode accepted by TurnServer")

	// 5. wait for bind response
	accesskey, deviceName, err := WaitForBindResponse(conn, 120*time.Second)
	if err != nil {
		return "", fmt.Errorf("binding failed: %w", err)
	}

	// 6. security confirmation
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Printf("WARNING! Are you sure you want to bind AccessKey from \"%s\"?\n", deviceName)
	fmt.Println("If this is NOT your own AccessKey, your device could be")
	fmt.Println("controlled by others.")
	fmt.Println("============================================================")
	fmt.Println("  1. Yes, bind this AccessKey")
	fmt.Println("  2. No, cancel")
	fmt.Println()

	for {
		fmt.Print("Select [1-2]: ")
		if !scanner.Scan() {
			return "", fmt.Errorf("failed to read input")
		}
		choice := scanner.Text()
		switch choice {
		case "1":
			return accesskey, nil
		case "2":
			return "", fmt.Errorf("binding cancelled")
		default:
			fmt.Println("Invalid choice, please enter 1 or 2.")
		}
	}
}

// renderQRBackground renders QR code using ANSI background colors (for Windows CMD)
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
