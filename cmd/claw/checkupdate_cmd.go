package main

import (
	"fmt"
	"strings"

	"github.com/CLIAnywhere/clianywhere_daemon/internal/checkupdate"
)

// selfBuildNotice is shown when a self-built (non-office) binary tries to
// apply an update — see buildtag_office.go / buildtag_default.go.
const selfBuildNotice = "This binary was built by you, not from an official release. To update, run git pull, then run the build script for your platform."

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
	if !isOfficeBuild {
		fmt.Println(selfBuildNotice)
		return
	}

	fmt.Printf("Updating %s -> %s...\n", res.Current, res.Latest)
	if err := checkupdate.ApplyUpdate(res); err != nil {
		fmt.Printf("Update failed: %v\n", err)
	}
}

// handleCheckUpdateInteractive is the main-menu "Check for Updates" entry:
// check, print details, ask for confirmation (Y), then apply. Runs in the
// CLI parent process — the update replaces the binary this process is
// running from; the new version takes effect after restart.
func handleCheckUpdateInteractive() {
	res, err := checkupdate.CheckUpdate(Version)
	if err != nil {
		fmt.Printf("Check update failed: %v\n", err)
		return
	}

	fmt.Printf("Current version: %s\n", res.Current)
	fmt.Printf("Latest  version: %s\n", res.Latest)
	if !res.Available {
		fmt.Println("Already up to date.")
		return
	}
	if !isOfficeBuild {
		// self-built binary: show the update but refuse to apply it
		fmt.Println(selfBuildNotice)
		return
	}
	if asset := checkupdate.PlatformAsset(res.Release); asset != nil {
		fmt.Printf("Package: %s (%d bytes)\n", asset.Name, asset.Size)
	}

	fmt.Print("Update now? [Y/n]: ")
	var answer string
	fmt.Scanln(&answer) // empty line (Enter) errors — treated as not confirmed
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		fmt.Println("Cancelled.")
		return
	}

	fmt.Printf("Updating %s -> %s...\n", res.Current, res.Latest)
	if err := checkupdate.ApplyUpdate(res); err != nil {
		fmt.Printf("Update failed: %v\n", err)
		return
	}
	// Success: on Windows ApplyUpdate never returns (installer + exit).
	// On Linux the binary was replaced; on macOS the dmg is open in Finder.
	fmt.Println(updateNoticeText())
	fmt.Println("Restart CLIAnywhere to finish.")
}
