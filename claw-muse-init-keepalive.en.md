# claw (CLIAnywhere Host Daemon) — Initialization & Keep-Alive Manual

> Audience: a Muse instance deploying claw on a Muse-managed VM.
> Goal: follow this manual to complete the **first-time initialization** and set up a **keep-alive scheduled task**, so claw stays online long-term.
> This manual is self-contained; no other context is needed. Paths use `/home/hatch` as the example home directory — adjust to the actual machine.

---

## 0. Background & Environment Constraints (read before touching anything)

claw is the host-side program of CLIAnywhere (CLI edition, a single binary). While running, it connects to the TurnServer (`wss://ts.clianywhere.com/ws`) and stays online so the user can reach this machine's terminal remotely from a CLIAnywhere client. Process model: the launcher process forks a daemon child and stays resident as its guardian; the two are a pair — kill either one and the other exits too. The daemon holds a single-instance lock (`~/.clianywhere/daemon.lock`); never start a second instance.

This environment (a Muse-managed VM) has three hard constraints. Verify each one empirically before proceeding:

1. **Direct egress is intercepted by the platform gateway.** On a direct connection the TLS handshake fails; the error fingerprint is
   `tls: first record does not look like a TLS handshake`.
   The only way out is the platform's HTTP CONNECT proxy: hostname `hatch-egress-proxy`, port `3128`, no authentication, recorded in the `HTTPS_PROXY` entry of `/etc/profile.d/hatch-egress.sh` (fallback: `/etc/environment`). Resolve the hostname to its current IPv4 before use (`getent ahostsv4 hatch-egress-proxy`). That IP may drift in theory, so **resolve it fresh on every start — never hard-code a cached value**.
2. **The platform reviews network access per destination.** The first time a process connects to a new domain, the gateway holds the connection until the user approves it on a permission card. If the wait exceeds the program's own connect timeout, that first attempt fails; once approved, the retry goes through. See §1.4 — you must warn the user before the first launch.
3. **The home directory (`~`) survives VM restarts and replacements; nothing outside it does.** A systemd unit lives in `/etc/systemd/system/` and is lost on machine replacement, so this design **does not rely on systemd**: every asset lives inside the home directory, and keep-alive is provided by a Muse scheduled task. Note also that individual small files in the home directory have, on occasion, gone missing without explanation — hence the backup copy of the critical file (see §1.2).

---

## 1. First-Time Initialization

### 1.1 Download the CLI binary from GitHub Releases

The CLIAnywhere daemon is open source on GitHub. Download the CLI build from Releases yourself; do not wait for the user to send you a file:

- Repository: `CLIAnywhere/clianywhere_daemon`, Releases page:
  https://github.com/CLIAnywhere/clianywhere_daemon/releases
- Download URLs (fixed URLs that always point at the CLI build of the latest release; pick by this machine's architecture — check with `uname -m` first):
  - x86_64: `https://github.com/CLIAnywhere/clianywhere_daemon/releases/latest/download/claw-linux-cli-amd64.zip`
  - aarch64: `https://github.com/CLIAnywhere/clianywhere_daemon/releases/latest/download/claw-linux-cli-arm64.zip`
- Download and install (inside this VM, curl can reach the internet through the platform proxy environment already present in the shell):

```bash
mkdir -p ~/workspace/claw-cli && cd ~/workspace/claw-cli
curl -fL -o claw-cli.zip "https://github.com/CLIAnywhere/clianywhere_daemon/releases/latest/download/claw-linux-cli-amd64.zip"
unzip -o claw-cli.zip
chmod +x claw
sha256sum claw   # record it for later verification
```

- **Version requirement: the build must support the `--proxy` flag.** Older builds without proxy support cannot connect in this environment at all (direct TLS is intercepted); do not try to work around this. There is no need to guess whether the build supports it: §1.4's first launch proves it empirically — the log must show `[PROXY] outbound via ...`. If even the latest release does not support `--proxy`, stop, report to the user, and ask the user for a newer build; do not improvise alternative egress schemes.

### 1.2 Ask the user for the accesskey and store it

The accesskey is this host's access credential. Only the user holds it; Muse does not have it, so you must ask the user for it.

- Example wording: "Please send me this machine's CLIAnywhere accesskey. I'll write it into the config file and start claw. The key is stored only in a local file and never written into any log."
- The user may send it directly in chat (the user's established habit: keys are sent directly and stored by Muse in a local file — no vault entry card).
- Store it:
  - `mkdir -p ~/.clianywhere && chmod 700 ~/.clianywhere`
  - Write the content verbatim to `~/.clianywhere/daemon.accesskey`, `chmod 600`.
  - **Make a backup copy**: copy it to `~/.secrets/clianywhere-daemon.accesskey.bak`, `chmod 600`. This is the only data source for the keep-alive mechanism's "rebuild the accesskey" step.
- Discipline: the key's value must **never** appear in any reply, log, memory, or task text. When you need to refer to it, give only its file path. claw's own log prints a masked prefix; that masking is done by claw itself and may be quoted.

### 1.3 Write the launcher script run.sh

Path: `~/workspace/claw-cli/run.sh` (`chmod +x`). Its duties: pin HOME (the daemon locates `~/.clianywhere` via HOME), resolve the proxy address fresh on every start, launch claw with `--proxy`, and record the proxy used this time into `proxy-state.txt` for later inspection. Reference implementation:

```bash
#!/bin/bash
set -u
export HOME="/home/hatch"
BASE="/home/hatch/workspace/claw-cli"
STATE_FILE="$BASE/proxy-state.txt"
LOG="$HOME/.clianywhere/daemon.log"
FALLBACK_PROXY="http://198.19.0.1:3128"

RAW_PROXY=""
if [ -f /etc/profile.d/hatch-egress.sh ]; then
  RAW_PROXY="$(grep -E '^export HTTPS_PROXY=' /etc/profile.d/hatch-egress.sh | head -1 | cut -d= -f2-)"
fi
if [ -z "$RAW_PROXY" ] && [ -f /etc/environment ]; then
  RAW_PROXY="$(grep -E '^HTTPS_PROXY=' /etc/environment | head -1 | cut -d= -f2-)"
fi

PROXY="$FALLBACK_PROXY"
if [ -n "$RAW_PROXY" ]; then
  hp="${RAW_PROXY#*://}"; hp="${hp##*@}"; hp="${hp%%/*}"
  host="${hp%%:*}"; port="${hp##*:}"
  [ "$port" = "$host" ] && port="3128"
  if [ -n "$host" ]; then
    ip="$(getent ahostsv4 "$host" 2>/dev/null | awk 'NR==1{print $1}')"
    if [ -n "$ip" ]; then PROXY="http://$ip:$port"; else PROXY="http://$host:$port"; fi
  fi
fi

printf 'proxy=%s\nsource=%s\nupdated=%s\n' "$PROXY" "${RAW_PROXY:-fallback}" "$(date -Is)" > "$STATE_FILE" 2>/dev/null || true

# Launch note: this build forks the daemon and turns the foreground process into
# an interactive menu controller, which spins printing the menu when there is no
# TTY. So: start it in the background, wait for the daemon's NEW
# "connected to turn server" log line, then stop the controller process.
# The daemon itself keeps running (its own menu offers
# "Exit (server keeps running, connect via app)").
OFFSET="$(stat -c%s "$LOG" 2>/dev/null || echo 0)"
"$BASE/claw" --proxy "$PROXY" >> "$BASE/claw-stdout.log" 2>&1 &
CTRL=$!
for i in $(seq 1 30); do
  if tail -c +$((OFFSET + 1)) "$LOG" 2>/dev/null | grep -q "connected to turn server"; then break; fi
  kill -0 "$CTRL" 2>/dev/null || break
  sleep 2
done
kill "$CTRL" 2>/dev/null || true
```

### 1.4 First launch and the access-review warning (critical step — do not skip)

**Before launching, warn the user** (example wording):

> "I'm starting claw now. The first time it connects to ts.clianywhere.com, the platform will show a network-access review card — this is the routine check for a new destination, please choose 'Always allow'. The first connection attempt will most likely time out and fail once while waiting for that approval; that is expected. Once you approve it, claw retries automatically and connects within seconds."

Then launch it, detached from the current session so it survives the session ending:

```bash
cd ~/workspace/claw-cli && setsid nohup ./run.sh > claw-stdout.log 2>&1 &
```

Expected behavior and how to judge it:

- The log `~/.clianywhere/daemon.log` should first show `[PROXY] outbound via http://...` and `[TS] connecting wss://ts.clianywhere.com/ws via proxy ...`, confirming the proxy is in effect.
- If the first attempt reports `read tcp ... ->198.19.0.1:3128: i/o timeout` (about 10 seconds), it was almost certainly waiting on the review card. Confirm the user approved it, then check whether the next retry turns into `[TS] connected to turn server`.
- If the log repeatedly shows `tls: first record does not look like a TLS handshake`, the traffic is not going through the proxy: check whether the binary build supports `--proxy` and whether run.sh actually passed it. Stop and report to the user; do not retry forever.
- **Never run `./claw` bare.** The current build forks the daemon and then turns the foreground process into an interactive menu controller; without a TTY it loops printing the menu forever (`failed to enter raw mode` plus the full menu text — megabytes of log within seconds). Always launch through run.sh, which stops the controller once the daemon is connected, leaving only the daemon process. If a bare run already happened: kill only the foreground controller (the parent process); the daemon child keeps running on its own — do not kill both.
- Pass criteria: daemon.log contains `[TS] connected to turn server`, exactly one claw process (the daemon) remains, and the local WebSocket port `127.0.0.1:56300` is listening.
- Finally, ask the user to connect in once from their CLIAnywhere client to confirm end-to-end.

### 1.5 Initialization completion checklist

- [ ] Binary in place (supports `--proxy`, sha256 recorded)
- [ ] `~/.clianywhere/daemon.accesskey` exists (600), backup in `~/.secrets/` (600)
- [ ] run.sh in place and executable
- [ ] First launch complete: user approved the ts.clianywhere.com review card, log shows connected
- [ ] User's client connected successfully in a real test

---

## 2. Keep-Alive Mechanism (Muse scheduled task)

Cadence: **every 30 minutes**. The task body below can be used as-is (adjust paths to the actual machine):

```
Check and keep alive this machine's CLIAnywhere host daemon (claw).
Perform these three checks in order:

1. accesskey integrity: if ~/.clianywhere/daemon.accesskey is missing
   or empty, or differs from the backup
   ~/.secrets/clianywhere-daemon.accesskey.bak (compare checksums;
   never print file contents), restore it by copying the backup back
   and resetting permissions to 600, and report "rebuilt".
   If the backup is also missing, report to the user immediately;
   never generate or guess a key.
2. process liveness: if no claw process is running, start it with
   cd ~/workspace/claw-cli && setsid nohup ./run.sh > claw-stdout.log 2>&1 &
   then wait about 10 seconds and re-check ~/.clianywhere/daemon.log.
   The start counts as successful only if a new
   "[TS] connected to turn server" line appears.
   Try at most 2 starts per round; if it still fails, report the error
   at the tail of the log and stop.
3. connection liveness: if the process exists but daemon.log shows
   only "[TS] connect ... failed" and no "connected" in the last
   10 minutes, treat the connection as dead: kill the existing claw
   processes (launcher and daemon are a pair; killing one makes the
   other exit), then restart and verify as in step 2.

Reporting rules: if everything is fine and no action was taken, stay
silent and do not disturb the user. If any repair action was taken
(rebuilt key, started, restarted), report in one or two short sentences
what was done and how it was verified. If something cannot be fixed,
report the blocker with the log evidence and stop, leaving the
decision to the user.
```

### Keep-alive acceptance tests (inject each fault and verify after setup)

1. Temporarily rename the accesskey → the next round must restore it automatically, with permissions 600;
2. Kill the claw process pair → the next round must restart it, and the log must show connected;
3. Run one round with everything healthy → no report at all (silence).

---

## 3. Security & Discipline

- The accesskey's value must never be written down: not in task text, logs, memory, or reports. The backup and the main file are the only two copies, both in mode-600 files; the keep-alive only ever `cp`s the file or compares checksums — it never reads the content for output.
- Single instance: do not start another claw by hand outside the keep-alive task; it will collide with daemon.lock. If an instance is already running, inspect the process pair's state before any manual action.
- Bounded retries: any automatic repair is attempted at most twice per round; if it cannot be fixed, stop and report — never write an unbounded retry loop.
- This manual authorizes actions within "initialization + keep-alive" only. Deleting the service, changing the key, sending information externally, and similar actions still require the user's confirmation on the spot.

## 4. Fault Fingerprint Quick Reference

| Symptom (in daemon.log) | Meaning | Action |
|---|---|---|
| `tls: first record does not look like a TLS handshake` repeatedly | Direct connection intercepted by the gateway; not using the proxy | Check the build supports `--proxy` and run.sh passes it |
| `read tcp ...:3128: i/o timeout`, only on first connect / new domain | Waiting on the platform review card | Remind the user to choose "Always allow"; the retry succeeds after approval |
| Several `failed` lines followed by `connected` | Normal (retrying, or recovered after approval) | No action |
| Process alive, but only `failed` for the last 10 minutes | Connection dead | Keep-alive step 3: restart the pair |
| Lock-related error immediately at startup | An instance is already running | Inspect processes first; do not force a second instance |
| accesskey file has vanished | A home-directory file was lost (has happened before) | Keep-alive step 1 restores from backup; if the backup is also gone, ask the user to re-send the key |
