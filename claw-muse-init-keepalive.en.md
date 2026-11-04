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

## 2. Keep-Alive Mechanism (script watchdog — important, do not use the wrong mechanism)

**Use a pure-shell script watchdog (a hook), never an agent scheduled task (cron).** The difference is money: an agent task wakes a model on every round to read the task, run the checks, and decide to stay silent — tokens are spent even when everything is healthy and nothing is reported, and most of the cost is fixed context overhead. A script watchdog runs the checks and the repairs itself without any model involved: **when everything is healthy, token cost is exactly zero.** Because idle rounds are free, set the cadence to **every 5 minutes** — worst-case recovery drops from 30 minutes to about 5.

How it works: a hook runs the script below every 5 minutes. The script performs three checks in order: (1) accesskey integrity — compare checksums against the backup (never read out the key's value); if missing or mismatched, copy the backup back and reset mode 600; if the backup is also missing, that is an unfixable problem. (2) Process liveness — if no claw process is running, start it via run.sh; success means a NEW `[TS] connected to turn server` line appears in daemon.log; at most 2 start attempts per round. (3) Connection liveness — if the process exists but has been up at least 10 minutes and the log shows only failures (no `connected`) in the last 10 minutes, treat the connection as dead: kill claw and restart as in step 2. If everything is healthy, the script ends with `silent` and wakes nobody. Only when it repaired something, or could not fix a problem, does it `wake` an agent, which then verifies the live state and reports to the user.

### 2.1 The watchdog script (full text; save to `~/hooks/scripts/claw-watchdog.sh` and `chmod +x`)

The `silent` / `wake` / `log` helpers come from the hook runtime (`$HATCH_HOOK_RUNTIME`). When `HATCH_HOOK_DRY_RUN=1`, the script only judges and performs no repairs.

```bash
#!/usr/bin/env bash
# claw-watchdog: keep-alive for the CLIAnywhere host daemon (claw).
# Pure shell — no model tokens while healthy. Repairs what it can itself;
# wakes the agent only when it repaired something or cannot fix a problem.
set -uo pipefail
source "$HATCH_HOOK_RUNTIME"

export HOME="/home/hatch"
KEY="$HOME/.clianywhere/daemon.accesskey"
BAK="$HOME/.secrets/clianywhere-daemon.accesskey.bak"
DLOG="$HOME/.clianywhere/daemon.log"
BASE="$HOME/workspace/claw-cli"
DRY="${HATCH_HOOK_DRY_RUN:-0}"

ACTIONS=""
PROBLEMS=""
note_action()  { ACTIONS="${ACTIONS:+$ACTIONS; }$1"; }
note_problem() { PROBLEMS="${PROBLEMS:+$PROBLEMS; }$1"; }
sanitize() { printf '%s' "$1" | tr -d '"\\' | tr '\n' ' '; }

last_epoch() { # $1 = pattern; prints epoch of last matching log line, or 0
  local line ts
  line="$(grep -F "$1" "$DLOG" 2>/dev/null | tail -1)" || true
  [ -n "${line:-}" ] || { echo 0; return; }
  ts="$(printf '%s' "$line" | grep -oE '[0-9]{4}/[0-9]{2}/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}' | head -1)"
  [ -n "$ts" ] || { echo 0; return; }
  date -d "$(printf '%s' "$ts" | sed 's|/|-|g')" +%s 2>/dev/null || echo 0
}

new_connected() { # $1 = byte offset of daemon.log before a start attempt
  tail -c +$(($1 + 1)) "$DLOG" 2>/dev/null | grep -q "connected to turn server"
}

start_claw() { # one start attempt via run.sh; returns 0 if a NEW connected line appears
  local off i
  off="$(stat -c%s "$DLOG" 2>/dev/null || echo 0)"
  if [ "$DRY" != "1" ]; then
    ( cd "$BASE" && setsid nohup ./run.sh > claw-stdout.log 2>&1 & )
  fi
  for i in $(seq 1 15); do
    [ "$DRY" = "1" ] || new_connected "$off" && return 0
    sleep 2
  done
  [ "$DRY" = "1" ] && return 1
  new_connected "$off"
}

# --- Check 1: accesskey integrity (compare only, never print contents) ---
if [ ! -s "$KEY" ] || ! cmp -s "$KEY" "$BAK" 2>/dev/null; then
  if [ -s "$BAK" ]; then
    if [ "$DRY" != "1" ]; then cp "$BAK" "$KEY"; chmod 600 "$KEY"; fi
    if cmp -s "$KEY" "$BAK" 2>/dev/null || [ "$DRY" = "1" ]; then
      note_action "accesskey was missing/mismatched and has been restored from backup (mode 600)"
    else
      note_problem "accesskey restore from backup failed"
    fi
  else
    note_problem "accesskey is missing/mismatched and the backup is also missing; user must re-send the key"
  fi
fi

# --- Checks 2 & 3: process and connection liveness ---
if ! pgrep -x claw >/dev/null 2>&1; then
  if start_claw || start_claw; then
    note_action "claw was not running; started via run.sh and verified a new connected line in daemon.log"
  else
    note_problem "claw is not running and start via run.sh did not produce a connected line; see tail of daemon.log"
  fi
else
  NOW="$(date +%s)"
  AGE="$(ps -o etimes= -p "$(pgrep -x claw | paste -sd,)" 2>/dev/null | sort -n | head -1 | tr -d ' ')"
  LAST_CONN="$(last_epoch 'connected to turn server')"
  LAST_FAIL="$(last_epoch 'failed')"
  if [ "${AGE:-0}" -ge 600 ] && [ $((NOW - LAST_CONN)) -gt 600 ] && [ "$LAST_FAIL" -gt "$LAST_CONN" ] && [ $((NOW - LAST_FAIL)) -le 900 ]; then
    if [ "$DRY" != "1" ]; then pkill -x claw || true; sleep 3; fi
    if start_claw || start_claw; then
      note_action "claw process was alive but connection dead (only failures in the last 10 minutes); restarted and verified connected"
    else
      note_problem "connection-dead restart attempted but no new connected line appeared; see tail of daemon.log"
    fi
  fi
fi

# --- Decision: exactly one of silent / wake ---
if [ -n "$PROBLEMS" ]; then
  wake "claw_watchdog_problem" "{\"actions\":\"$(sanitize "$ACTIONS")\",\"problems\":\"$(sanitize "$PROBLEMS")\"}"
elif [ -n "$ACTIONS" ]; then
  wake "claw_watchdog_repaired" "{\"actions\":\"$(sanitize "$ACTIONS")\",\"problems\":\"\"}"
else
  silent "healthy" '{}'
fi
```

### 2.2 Register the hook

- id: `claw-watchdog-hook`; script: `~/hooks/scripts/claw-watchdog.sh`; **poll interval: 300 seconds (5 minutes)**; script timeout: 180 seconds.
- A new hook starts disabled: `dry_run` it first (a healthy machine must return `silent` / `healthy`), then `enable` it.
- The wake prompt for the agent must require: (1) independently verify the current state (claw process, ports 127.0.0.1:56300 and 56881, newest daemon.log lines, accesskey present with mode 600 — never read out the key's value); (2) report to the user in one or two short sentences what the watchdog did or failed to do and the verified current state; (3) if still down, at most one more start via run.sh, then report the outcome with log evidence — no retry loops.



## 3. Security & Discipline

- The accesskey's value must never be written down: not in task text, logs, memory, or reports. The backup and the main file are the only two copies, both in mode-600 files; the keep-alive only ever `cp`s the file or compares checksums — it never reads the content for output.
- Single instance: do not start another claw by hand outside the keep-alive task; it will collide with daemon.lock. If an instance is already running, inspect its process state before any manual action.
- Bounded retries: any automatic repair is attempted at most twice per round; if it cannot be fixed, stop and report — never write an unbounded retry loop.
- This manual authorizes actions within "initialization + keep-alive" only. Deleting the service, changing the key, sending information externally, and similar actions still require the user's confirmation on the spot.

## 4. Fault Fingerprint Quick Reference

| Symptom (in daemon.log) | Meaning | Action |
|---|---|---|
| `tls: first record does not look like a TLS handshake` repeatedly | Direct connection intercepted by the gateway; not using the proxy | Check the build supports `--proxy` and run.sh passes it |
| `read tcp ...:3128: i/o timeout`, only on first connect / new domain | Waiting on the platform review card | Remind the user to choose "Always allow"; the retry succeeds after approval |
| Several `failed` lines followed by `connected` | Normal (retrying, or recovered after approval) | No action |
| Process alive, but only `failed` for the last 10 minutes | Connection dead | Watchdog check 3: kill and restart it |
| Lock-related error immediately at startup | An instance is already running | Inspect processes first; do not force a second instance |
| accesskey file has vanished | A home-directory file was lost (has happened before) | Keep-alive step 1 restores from backup; if the backup is also gone, ask the user to re-send the key |
