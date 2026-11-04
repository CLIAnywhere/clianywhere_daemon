# claw (CLIAnywhere Host Daemon) — Initialization & Keep-Alive Manual

> Audience: this assistant (`clianywhere_daemon`) deploying claw on Grok Bot's own computer (the box).
> Goal: complete **first-time initialization** and set up a **keep-alive routine** so claw stays online long-term.
> This manual is self-contained. Paths: `HOME=/home/box`, install under `/workspace/claw-cli`.

---

## 0. Background

claw is the host-side program of CLIAnywhere (CLI edition, a single binary). While running, it connects to the TurnServer (`wss://ts.clianywhere.com/ws`) and stays online so the user can reach this machine's terminal remotely from a CLIAnywhere client.

Process model: the launcher process forks a daemon child and stays resident as its guardian; the two are a pair — kill either one and the other exits too. The daemon holds a single-instance lock (`~/.clianywhere/daemon.lock`); never start a second instance.

Environment notes for this box:

1. **Direct egress.** Start claw without `--proxy`. Do not invent alternative egress schemes. If connect fails with repeated TLS handshake errors, stop and report to the user.
2. **Paths.** Home is `/home/box`. Install and scripts live in `/workspace/claw-cli`. Config and key live under `~/.clianywhere/`. Do not rely on systemd; keep-alive is a Grok Bot Routine. After the box is updated or reset, re-run this manual if files are gone.
3. **Key discipline.** The accesskey value must never appear in replies, logs, memory, or routine text — cite file paths only.

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

---

## 2. Keep-Alive Mechanism (Grok Bot Routine)

Cadence: **every 30 minutes**. Create a Grok Bot Routine (not systemd).

Suggested cron (user local time Asia/Shanghai): `*/30 * * * *`, flexible timing is fine.

Use this body as the Routine prompt:

```
Check and keep alive this machine's CLIAnywhere host daemon (claw) on the Grok Bot box.
Use scripts / shell for the checks; only speak to the user when something was repaired or cannot be fixed.
Perform these three checks in order:

1. accesskey integrity: if ~/.clianywhere/daemon.accesskey is missing
   or empty, or differs from the backup
   ~/.secrets/clianywhere-daemon.accesskey.bak (compare checksums;
   never print file contents), restore it by copying the backup back
   and resetting permissions to 600, and report "rebuilt".
   If the backup is also missing, report to the user immediately;
   never generate or guess a key.
2. process liveness: if no claw process is running, start it with
   cd /workspace/claw-cli && setsid nohup ./run.sh > claw-stdout.log 2>&1 &
   then wait about 10 seconds and re-check ~/.clianywhere/daemon.log.
   The start counts as successful only if a new
   "[TS] connected to turn server" line appears.
   Try at most 2 starts per round; if it still fails, report the error
   at the tail of the log and stop.
   Do NOT pass --proxy and do NOT invent egress workarounds.
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

Prefer scripts for checks and restarts; use the LLM only to report or judge anomalies.

### Keep-alive acceptance tests (inject each fault after setup)

1. Temporarily rename the accesskey → the next round must restore it automatically, permissions 600;
2. Kill the claw process pair → the next round must restart it, and the log must show connected;
3. Run one round with everything healthy → no report at all (silence).

---

## 3. Security & Discipline

- The accesskey value must never be written down: not in task text, logs, memory, or reports. Main file and backup are the only two copies, both mode 600; keep-alive only `cp`s or compares checksums — never prints contents.
- Single instance: do not start another claw by hand outside the keep-alive routine; it will collide with `daemon.lock`. If an instance is already running, inspect the process pair before any manual action.
- Bounded retries: at most two automatic repairs per round; if it cannot be fixed, stop and report — never write an unbounded retry loop.
- This manual authorizes "initialization + keep-alive" only. Deleting the service, changing the key, sending information externally, and similar actions still require the user's confirmation on the spot.

## 4. Fault Fingerprint Quick Reference

| Symptom (in daemon.log) | Meaning | Action |
|---|---|---|
| `tls: first record does not look like a TLS handshake` repeatedly | Direct connect failed | Stop; report to the user. Do not add `--proxy` |
| Several `failed` lines followed by `connected` | Normal (retrying / recovered) | No action |
| Process alive, but only `failed` for the last 10 minutes | Connection dead | Keep-alive step 3: restart the pair |
| Lock-related error immediately at startup | An instance is already running | Inspect processes first; do not force a second instance |
| accesskey file has vanished | Home file lost | Keep-alive step 1 restores from backup; if backup is also gone, ask the user to re-send the key |
