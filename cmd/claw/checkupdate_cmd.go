package main

import (
	"fmt"

	"github.com/CLIAnywhere/clianywhere_daemon/internal/checkupdate"
)

// handleCheckUpdate implements the "checkupdate" subcommand: query the latest
// GitHub release and report whether an update is available.
func handleCheckUpdate() {
	res, err := checkupdate.CheckUpdate(Version)
	if err != nil {
		fmt.Printf("Check update failed: %v\n", err)
		return
	}

	fmt.Printf("Current version: %s\n", res.Current)
	fmt.Printf("Latest  version: %s\n", res.Latest)
	if res.Available {
		fmt.Println("Update available.")
		if asset := checkupdate.PlatformAsset(res.Release); asset != nil {
			fmt.Printf("Package: %s (%d bytes)\n", asset.Name, asset.Size)
		}
	} else {
		fmt.Println("Already up to date.")
	}
}

// handleUpdate implements the "update" subcommand: check for a newer release,
// download the platform package and apply it (platform-specific behavior,
// see internal/checkupdate).
func handleUpdate() {
	res, err := checkupdate.CheckUpdate(Version)
	if err != nil {
		fmt.Printf("Check update failed: %v\n", err)
		return
	}
	if !res.Available {
		fmt.Printf("Already up to date (%s).\n", res.Current)
		return
	}

	fmt.Printf("Updating %s -> %s...\n", res.Current, res.Latest)
	if err := checkupdate.ApplyUpdate(res); err != nil {
		fmt.Printf("Update failed: %v\n", err)
	}
}
