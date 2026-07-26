# CLIAnywhere Daemon (`claw`)

The daemon that runs on your own machine and lets the **CLIAnywhere** mobile/web app
securely access a local terminal from anywhere.

Pair the daemon with the **CLIAnywhere app** (download from <https://www.clianywhere.com>).

## How it behaves

- **First run** — starts the daemon in the background **and** opens the management UI.
- **Subsequent runs** — the daemon is already running, so only the management UI opens.
- To stop the daemon completely, use the management UI (or kill the `claw` process).

## Build from source

You need **Go 1.25+** (<https://go.dev/dl/>).

### Windows

1. Install Go 1.25+.
2. Double-click `build-windows.bat`.
3. The build produces `claw.exe` in this folder. Double-click it to run.

### macOS

1. Install Go 1.25+ (easiest via Homebrew: `brew install go`).
2. Run the build script:

   ```sh
   chmod +x build-macos.sh
   ./build-macos.sh
   ```

   Or double-click `build-macos.sh` in Finder.

3. The build produces `claw` in this folder. Double-click it (or run `./claw`) to start.

#### macOS Gatekeeper note

If you downloaded this repository as a ZIP from the browser, macOS attaches a
quarantine flag and may block the script / binary from running. Remove it once
with:

```sh
xattr -dr com.apple.quarantine /path/to/this/folder
```

Then run the build script (or the resulting `claw`) again.
