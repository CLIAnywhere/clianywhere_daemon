# claw (CLIAnywhere Host Daemon) — Initialization & Keep-Alive Manual

> Audience: this assistant (`clianywhere_daemon`) deploying claw on Grok Bot's own computer (the box).
> Goal: complete **first-time initialization** and set up a **script-based keep-alive** that checks **every 5 minutes** so claw stays online long-term **without waking an LLM on every check**.
> This manual is self-contained. Paths: `HOME=/home/box`, install under `/workspace/claw-cli`.

---

## 0. Background

claw is the host-side program of CLIAnywhere (CLI edition, a single binary). While running, it connects to the TurnServer (`wss://ts.clianywhere.com/ws`) and stays online so the user can reach this machine's terminal remotely from a CLIAnywhere client.

Process model: the launcher process forks a daemon child and stays resident as its guardian; the two are a pair — kill either one and the other exits too. The daemon holds a single-instance lock (`~/.clianywhere/daemon.lock`); never start a second instance.

Environment notes for this box:

1. **Direct egress.** Start claw without `--proxy`. Do not invent alternative egress schemes. If connect fails with repeated TLS handshake errors, stop and report to the user.
2. **Paths.** Home is `/home/box`. Install and scripts live in `/workspace/claw-cli`. Config and key live under `~/.clianywhere/`. Do not rely on systemd. **Keep-alive must be a local shell script loop every 5 minutes**, not a Grok Bot LLM Routine for the regular checks. After the box is updated or reset, re-run this manual if files are gone.
3. **Key discipline.** The accesskey value must never appear in replies, logs, memory, or routine text — cite file paths only.
4. **Token discipline.** Prefer scripts for keep-alive. Do **not** schedule an agent/LLM wake every few minutes just to poll claw. LLM wakes are only for first-time setup, user-asked diagnosis, or rare escalation when the script cannot repair.

---

## 1. First-Time Initialization

### 1.1 Download the CLI binary from GitHub Releases

Download the CLI build yourself; do not wait for the user to send a file.

- Repository: `CLIAnywhere/clianywhere_daemon`  
  https://github.com/CLIAnywhere/clianywhere_daemon/releases
- Pick by architecture (`uname -m` first):
  - x86_64: `https://github.com/CLIAnywhere/clianywhere_daemon/releases/latest/download/claw-linux-cli-amd64.zip`
  - aarch64: `https://github.com/CLIAnywhere/clianywhere_daemon/releases/latest/download/claw-linux-cli-arm64.zip`
- Install (amd64 when this box is x86_64):

```bash
mkdir -p /workspace/claw-cli && cd /workspace/claw-cli
curl -fL -o claw-cli.zip "https://github.com/CLIAnywhere/clianywhere_daemon/releases/latest/download/claw-linux-cli-amd64.zip"
unzip -o claw-cli.zip
chmod +x claw
sha256sum claw   # record it for later verification
```

### 1.2 Ask the user for the accesskey and store it

The accesskey is this host's credential. Only the user has it; you must ask.

- Example wording: "Please send me this machine's CLIAnywhere accesskey. I'll write it into the local config file and start claw. The key is stored only in a local file and never written into any log."
- Store it:
  - `mkdir -p ~/.clianywhere && chmod 700 ~/.clianywhere`
  - Write the content verbatim to `~/.clianywhere/daemon.accesskey`, `chmod 600`
  - **Make a backup**:  
    `mkdir -p ~/.secrets && cp ~/.clianywhere/daemon.accesskey ~/.secrets/clianywhere-daemon.accesskey.bak && chmod 600 ~/.secrets/clianywhere-daemon.accesskey.bak`  
    The backup is the only data source for the keep-alive "rebuild the accesskey" step.
- The key's value must never appear in any reply, log, memory, or task text. Refer to it by path only. claw's own masked log prefix may be quoted.

### 1.3 Write the launcher script `run.sh`

Path: `/workspace/claw-cli/run.sh` (`chmod +x`). Pin `HOME`, then start claw directly.

```bash
#!/bin/bash
set -u
export HOME="/home/box"
BASE="/workspace/claw-cli"
exec "$BASE/claw"
```

Do not pass `--proxy`.

### 1.4 First launch and acceptance

```bash
cd /workspace/claw-cli && setsid nohup ./run.sh > claw-stdout.log 2>&1 &
```

How to judge:

- `~/.clianywhere/daemon.log` should show TurnServer activity and end with `[TS] connected to turn server`.
- If the log repeatedly shows `tls: first record does not look like a TLS handshake`, stop and report to the user; do not add a proxy on your own.
- Pass criteria: daemon.log contains `[TS] connected to turn server`, the claw process pair (launcher + daemon) exists, and `127.0.0.1:56300` is listening.
- Ask the user to connect once from their CLIAnywhere client for an end-to-end check.

### 1.5 Initialization completion checklist

- [ ] Binary in place (sha256 recorded)
- [ ] `~/.clianywhere/daemon.accesskey` exists (600), backup in `~/.secrets/` (600)
- [ ] `/workspace/claw-cli/run.sh` executable, no `--proxy`
- [ ] First launch complete: log shows connected, port 56300 listening
- [ ] User's client connected successfully in a real test
- [ ] Script keep-alive installed: `keepalive.sh` + `keepalive-loop.sh`, loop running, **every 5 minutes**

---

## 2. Keep-Alive Mechanism (script every 5 minutes — required)

**This is the required keep-alive design.**

- Cadence: **every 5 minutes** (`sleep 300` in a detached loop).
- Vehicle: **local shell scripts only** under `/workspace/claw-cli/`.
- Do **not** use a Grok Bot Routine / LLM agent wake for the regular 5-minute poll (wastes tokens). Pause or delete any old agent keep-alive routine once the script loop is running.
- This box often has **no `crontab`**. Prefer the detached loop below. If `crontab` exists, `*/5 * * * * /workspace/claw-cli/keepalive.sh` is also fine — still script-only, never LLM.

### 2.1 What each check round must do

`keepalive.sh` runs these three checks in order (no LLM):

1. **accesskey integrity**: if `~/.clianywhere/daemon.accesskey` is missing/empty or differs from `~/.secrets/clianywhere-daemon.accesskey.bak` (compare checksums / `cmp`; never print file contents), restore from backup and `chmod 600`. If the backup is also missing, write a failure status and stop — never generate or guess a key.
2. **process liveness**: if no claw process is running, start with  
   `cd /workspace/claw-cli && setsid nohup ./run.sh > claw-stdout.log 2>&1 &`  
   wait ~10 seconds, and require a new `[TS] connected to turn server` line. At most **2** start attempts per round.
3. **connection liveness**: if the process exists but there is no `[TS] connected to turn server` in the last **10 minutes**, kill the claw pair and restart as in step 2.

Do **not** pass `--proxy`. Do **not** invent egress workarounds.

Write a short status file to `/workspace/claw-cli/keepalive-status.txt` (`result=ok|repaired|fail`, actions, blockers). Do not message the user from the script.

### 2.2 Install `keepalive.sh` and the 5-minute loop

Place both scripts under `/workspace/claw-cli/`, `chmod +x`.

`keepalive-loop.sh` (detached supervisor; runs the check **every 5 minutes**):

```bash
#!/bin/bash
set -u
export HOME="/home/box"
BASE="/workspace/claw-cli"
LOCK="$BASE/keepalive-loop.lock"
exec 9>"$LOCK"
if ! flock -n 9; then
  echo "keepalive-loop already running" >&2
  exit 0
fi
while true; do
  "$BASE/keepalive.sh" >> "$BASE/keepalive-cron.log" 2>&1 || true
  sleep 300
done
```

Start it once (and only once):

```bash
pkill -f '/workspace/claw-cli/keepalive-loop.sh' 2>/dev/null || true
cd /workspace/claw-cli && setsid nohup ./keepalive-loop.sh > keepalive-loop.out 2>&1 &
```

Verify:

- `pgrep -af keepalive-loop.sh` shows one loop
- After a round, `keepalive-status.txt` exists with `result=ok` (or `repaired` after a fix)
- Interval is **5 minutes** (`sleep 300`), not 30 minutes

Reference implementation of `keepalive.sh` should live beside these files in `/workspace/claw-cli/keepalive.sh` and implement §2.1. Keep repairs bounded (≤2 starts per round).

### 2.3 When (not) to involve the LLM

- Healthy script rounds: **silence** — no chat, no routine wake.
- User asks “is claw up?”: read `keepalive-status.txt`, processes, and `daemon.log`; answer briefly.
- Script cannot repair (missing backup key, repeated start failure): tell the user with log evidence; do not spin forever.
- Do **not** recreate an every-5-minute Grok Bot Routine for keep-alive.



---

## 3. Security & Discipline

- The accesskey value must never be written down: not in task text, logs, memory, or reports. Main file and backup are the only two copies, both mode 600; keep-alive only `cp`s or compares checksums — never prints contents.
- Single instance: do not start another claw by hand outside keep-alive; it will collide with `daemon.lock`. If an instance is already running, inspect the process pair before any manual action.
- Bounded retries: at most two automatic repairs per round; if it cannot be fixed, stop and report — never write an unbounded retry loop inside one check.
- The 5-minute loop itself is intentional and long-running; that is not an unbounded repair loop.
- This manual authorizes "initialization + script keep-alive" only. Deleting the service, changing the key, sending information externally, and similar actions still require the user's confirmation on the spot.

## 4. Fault Fingerprint Quick Reference

| Symptom (in daemon.log) | Meaning | Action |
|---|---|---|
| `tls: first record does not look like a TLS handshake` repeatedly | Direct connect failed | Stop; report to the user. Do not add `--proxy` |
| Several `failed` lines followed by `connected` | Normal (retrying / recovered) | No action |
| Process alive, but no `connected` for the last 10 minutes | Connection dead | Script step 3: restart the pair |
| Lock-related error immediately at startup | An instance is already running | Inspect processes first; do not force a second instance |
| accesskey file has vanished | Home file lost | Script step 1 restores from backup; if backup is also gone, ask the user to re-send the key |
| `keepalive-loop.sh` not running | 5-minute checks stopped | Restart the loop (§2.2); do not replace it with an LLM routine |
