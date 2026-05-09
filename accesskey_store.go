package main

import (
	"os"
	"path/filepath"
	"strings"
)

const accessKeyDir = ".clianywhere"
const accessKeyFile = "daemon.accesskey"

// accesskeyFilePath returns the path to the accesskey file: ~/.clianywhere/daemon.accesskey
func accesskeyFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, accessKeyDir, accessKeyFile), nil
}

// ensureDir creates ~/.clianywhere/ directory if it doesn't exist
func ensureDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(home, accessKeyDir), 0700)
}

// saveAccessKey writes the accesskey to file with 0600 permissions
func saveAccessKey(key string) error {
	if err := ensureDir(); err != nil {
		return err
	}
	path, err := accesskeyFilePath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(key), 0600)
}

// loadAccessKey reads the accesskey from file, returns empty string if not found or empty
func loadAccessKey() (string, error) {
	path, err := accesskeyFilePath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
