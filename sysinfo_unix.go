//go:build !windows

package main

import "fmt"

func getWindowsVersion() (string, error) {
	return "", fmt.Errorf("not windows")
}
