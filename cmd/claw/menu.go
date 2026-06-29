//go:build cli

package main

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/term"
)

// showArrowKeyMenu displays an interactive arrow-key selection menu.
// Returns the selected index (0-based). Returns -1 if input fails.
// Uses raw terminal mode: ↑↓ to navigate, Enter to confirm.
func showArrowKeyMenu(title string, items []string) int {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		// fallback: non-interactive selection
		fmt.Println()
		fmt.Println(title)
		for i, item := range items {
			fmt.Printf("  %d. %s\n", i+1, item)
		}
		return 0
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	selected := 0
	draw := func() {
		fmt.Print("\033[H\033[2J") // clear screen
		fmt.Printf("\033[1;36m%s\033[0m\r\n", title)
		fmt.Print("═══════════════════════════════════\r\n")
		for i, item := range items {
			if i == selected {
				fmt.Printf(" \033[7m> %s\033[0m\r\n", item)
			} else {
				fmt.Printf("   %s\r\n", item)
			}
		}
		fmt.Print("═══════════════════════════════════\r\n")
		fmt.Print("\033[2m↑↓:select  Enter:confirm\033[0m\r\n")
	}

	draw()

	buf := make([]byte, 3)
	for {
		n, _ := os.Stdin.Read(buf)
		if n == 1 {
			switch buf[0] {
			case 0x0D: // Enter
				return selected
			case 0x03, 'q': // Ctrl+C or q
				return -1
			case 'j':
				selected = (selected + 1) % len(items)
				draw()
			case 'k':
				selected = (selected - 1 + len(items)) % len(items)
				draw()
			}
		} else if n == 3 && buf[0] == 0x1B && buf[1] == '[' {
			switch buf[2] {
			case 'A': // ↑
				selected = (selected - 1 + len(items)) % len(items)
				draw()
			case 'B': // ↓
				selected = (selected + 1) % len(items)
				draw()
			}
		}
	}
}

// pressAnyKeyToExit shows "Press any key to exit..." and waits for input (Windows only)
func pressAnyKeyToExit() {
	if runtime.GOOS == "windows" {
		fmt.Println()
		fmt.Println("Press any key to exit...")
		buf := make([]byte, 1)
		os.Stdin.Read(buf)
	}
}
