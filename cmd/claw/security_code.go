package main

import (
	"os"
	"path/filepath"
	"strings"
)

const secCodeFile = "security_code"

// isValidSecCode returns true if code is exactly 6 digits
func isValidSecCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// secCodeFilePath returns ~/.clianywhere/security_code
func secCodeFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, accessKeyDir, secCodeFile), nil
}

// SaveSecurityCode validates and writes the 6-digit code to file
func SaveSecurityCode(code string) error {
	if !isValidSecCode(code) {
		return os.ErrInvalid
	}
	if err := ensureDir(); err != nil {
		return err
	}
	path, err := secCodeFilePath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(code), 0600)
}

// LoadSecurityCode reads the code from file, returns empty string if not set or invalid
func LoadSecurityCode() string {
	path, err := secCodeFilePath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	code := strings.TrimSpace(string(data))
	if !isValidSecCode(code) {
		return ""
	}
	return code
}

// ClearSecurityCode deletes the security code file
func ClearSecurityCode() error {
	path, err := secCodeFilePath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// HasSecurityCode returns true if a valid security code is set
func HasSecurityCode() bool {
	return LoadSecurityCode() != ""
}
