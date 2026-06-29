//go:build !windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/creack/pty"
)

// unixPty Unix PTY implementation based on creack/pty
type unixPty struct {
	f          *os.File
	cmd        *exec.Cmd
	initFile   string // temp bash --init-file, cleaned up on Close
}

func (p *unixPty) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *unixPty) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *unixPty) Close() error {
	if p.initFile != "" {
		_ = os.Remove(p.initFile)
	}
	return p.f.Close()
}
func (p *unixPty) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}
func (p *unixPty) Wait() error { return p.cmd.Wait() }

// startPty starts a PTY-backed shell session.
//
// loginShell controls whether the shell is launched as a login shell (-l).
// On bash, login shells only read ~/.bash_profile (not ~/.bashrc), which means
// distro-provided color aliases (ls --color=auto, grep --color=auto, dircolors)
// are silently lost and the terminal renders without colors. To fix this we
// pass bash a generated --init-file that sources /etc/bash.bashrc + ~/.bashrc,
// and additionally sources /etc/profile + ~/.bash_profile when loginShell is
// requested, so users get both PATH setup and color aliases regardless of the
// login toggle.
func startPty(shellPath, dir string, cols, rows int, loginShell bool) (ptyConn, int, error) {
	base := filepath.Base(shellPath)
	var args []string
	var initFile string

	switch base {
	case "bash":
		// Generate a temp init script so bash always reads ~/.bashrc.
		// --rcfile is for non-login interactive bash; for login bash the
		// corresponding flag is --init-file (both behave identically here).
		var err error
		initFile, err = writeBashInitFile(loginShell)
		if err != nil {
			return nil, 0, fmt.Errorf("write bash init file: %w", err)
		}
		args = []string{"--init-file", initFile}
	case "zsh":
		// zsh reads ~/.zshenv → ~/.zprofile (login) → ~/.zshrc (always, interactive)
		// so colors work without intervention in both modes.
		if loginShell {
			args = []string{"-l"}
		}
	case "fish":
		// fish always reads ~/.config/fish/config.fish; no flag needed.
	case "sh", "dash":
		if loginShell {
			args = []string{"-l"}
		}
	default:
		// Unknown shell: honor loginShell, fall back to plain interactive otherwise.
		if loginShell {
			args = []string{"-l"}
		}
	}

	cmd := exec.Command(shellPath, args...)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"IS_CLIANYWHERE_PTY=1",
		"LS_COLORS=rs=0:di=01;34:ln=01;36:mh=00:pi=40;33:so=01;35:do=01;35:bd=40;33;01:cd=40;33;01:or=40;31;01:mi=00:su=37;41:sg=30;43:ca=30;41:tw=30;42:ow=34;42:st=37;44:ex=01;32:*.tar=01;31:*.tgz=01;31:*.arc=01;31:*.arj=01;31:*.taz=01;31:*.lha=01;31:*.lz4=01;31:*.lzh=01;31:*.lzma=01;31:*.tlz=01;31:*.txz=01;31:*.tzo=01;31:*.t7z=01;31:*.zip=01;31:*.z=01;31:*.Z=01;31:*.dz=01;31:*.gz=01;31:*.lrz=01;31:*.lz=01;31:*.lzo=01;31:*.xz=01;31:*.bz2=01;31:*.bz=01;31:*.tbz=01;31:*.tbz2=01;31:*.tz=01;31:*.deb=01;31:*.rpm=01;31:*.jar=01;31:*.war=01;31:*.ear=01;31:*.sar=01;31:*.rar=01;31:*.alz=01;31:*.ace=01;31:*.zoo=01;31:*.cpio=01;31:*.7z=01;31:*.rz=01;31:*.cab=01;31:*.jpg=01;35:*.jpeg=01;35:*.gif=01;35:*.bmp=01;35:*.pbm=01;35:*.pgm=01;35:*.ppm=01;35:*.tga=01;35:*.xbm=01;35:*.xpm=01;*.tif=01;35:*.tiff=01;35:*.png=01;35:*.svg=01;35:*.mjpg=01;35:*.mjpeg=01;35:*.m2v=01;35:*.mkv=01;35:*.webm=01;35:*.ogm=01;35:*.mp4=01;35:*.m4v=01;35:*.mpg=01;35:*.mpeg=01;35:*.mov=01;35:*.avi=01;35:*.wmv=01;35:*.flv=01;35:*.webp=01;35:*.pdf=00;33:*.ps=00;33:*.txt=00;33:*.md=00;33:*.json=00;33:*.yaml=00;33:*.yml=00;33:*.toml=00;33:*.conf=00;33:*.sh=01;32:*.bash=01;32:*.zsh=01;32:*.py=01;32:*.go=01;32:*.rs=01;32:*.js=01;32:*.ts=01;32",
	)
	if dir != "" {
		cmd.Dir = dir
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		if initFile != "" {
			_ = os.Remove(initFile)
		}
		return nil, 0, fmt.Errorf("start PTY failed: %w", err)
	}
	return &unixPty{f: ptmx, cmd: cmd, initFile: initFile}, cmd.Process.Pid, nil
}

// writeBashInitFile creates a temp init script for bash --init-file.
//
// Bash treats the file passed via --init-file/--rcfile as the interactive
// startup file (replacing ~/.bashrc). We use it to:
//   - source system + user bashrc (color aliases, dircolors, completions)
//   - when login mode is requested, also source /etc/profile and
//     ~/.bash_profile (or ~/.profile) so any PATH exports (nvm, rbenv, …)
//     still take effect
func writeBashInitFile(loginShell bool) (string, error) {
	var b strings.Builder
	b.WriteString("# CLIAnywhere bash init (auto-generated, safe to delete)\n")
	// Login-equivalent profile loading (only when requested).
	if loginShell {
		b.WriteString("[ -r /etc/profile ] && . /etc/profile\n")
		b.WriteString("if [ -r \"$HOME/.bash_profile\" ]; then\n")
		b.WriteString("  . \"$HOME/.bash_profile\"\n")
		b.WriteString("elif [ -r \"$HOME/.bash_login\" ]; then\n")
		b.WriteString("  . \"$HOME/.bash_login\"\n")
		b.WriteString("elif [ -r \"$HOME/.profile\" ]; then\n")
		b.WriteString("  . \"$HOME/.profile\"\n")
		b.WriteString("fi\n")
	}
	// Always source rc files so color aliases / dircolors apply.
	b.WriteString("[ -r /etc/bash.bashrc ] && . /etc/bash.bashrc\n")
	b.WriteString("[ -r \"$HOME/.bashrc\" ] && . \"$HOME/.bashrc\"\n")

	// Fall back to dircolors if LS_COLORS wasn't populated by the rc files.
	b.WriteString("if [ -z \"$LS_COLORS\" ] && command -v dircolors >/dev/null 2>&1; then\n")
	b.WriteString("  eval \"$(dircolors -b)\"\n")
	b.WriteString("fi\n")

	// Ensure common color aliases exist even if the rc files omitted them
	// (we don't override aliases the user already defined).
	b.WriteString("alias ls='ls --color=auto' 2>/dev/null\n")
	b.WriteString("alias grep='grep --color=auto' 2>/dev/null\n")

	// Random suffix to avoid collisions across sessions.
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	name := fmt.Sprintf(".clianywhere-bash-init-%d-%s", os.Getpid(), hex.EncodeToString(buf[:]))
	path := filepath.Join(os.TempDir(), name)
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		return "", err
	}
	return path, nil
}

func getExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return 1
}
