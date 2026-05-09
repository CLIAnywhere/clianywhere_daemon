//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// unixPty Unix PTY implementation based on creack/pty
type unixPty struct {
	f   *os.File
	cmd *exec.Cmd
}

func (p *unixPty) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *unixPty) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *unixPty) Close() error                { return p.f.Close() }
func (p *unixPty) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}
func (p *unixPty) Wait() error { return p.cmd.Wait() }

func startPty(shellPath, dir string, cols, rows int) (ptyConn, int, error) {
	cmd := exec.Command(shellPath, "-l")
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"IS_CLIANYWHERE_PTY=1",
		"LS_COLORS=rs=0:di=01;34:ln=01;36:mh=00:pi=40;33:so=01;35:do=01;35:bd=40;33;01:cd=40;33;01:or=40;31;01:mi=00:su=37;41:sg=30;43:ca=30;41:tw=30;42:ow=34;42:st=37;44:ex=01;32:*.tar=01;31:*.tgz=01;31:*.arc=01;31:*.arj=01;31:*.taz=01;31:*.lha=01;31:*.lz4=01;31:*.lzh=01;31:*.lzma=01;31:*.tlz=01;31:*.txz=01;31:*.tzo=01;31:*.t7z=01;31:*.zip=01;31:*.z=01;31:*.Z=01;31:*.dz=01;31:*.gz=01;31:*.lrz=01;31:*.lz=01;31:*.lzo=01;31:*.xz=01;31:*.bz2=01;31:*.bz=01;31:*.tbz=01;31:*.tbz2=01;31:*.tz=01;31:*.deb=01;31:*.rpm=01;31:*.jar=01;31:*.war=01;31:*.ear=01;31:*.sar=01;31:*.rar=01;31:*.alz=01;31:*.ace=01;31:*.zoo=01;31:*.cpio=01;31:*.7z=01;31:*.rz=01;31:*.cab=01;31:*.jpg=01;35:*.jpeg=01;35:*.gif=01;35:*.bmp=01;35:*.pbm=01;35:*.pgm=01;35:*.ppm=01;35:*.tga=01;35:*.xbm=01;35:*.xpm=01;35:*.tif=01;35:*.tiff=01;35:*.png=01;35:*.svg=01;35:*.mjpg=01;35:*.mjpeg=01;35:*.m2v=01;35:*.mkv=01;35:*.webm=01;35:*.ogm=01;35:*.mp4=01;35:*.m4v=01;35:*.mpg=01;35:*.mpeg=01;35:*.mov=01;35:*.avi=01;35:*.wmv=01;35:*.flv=01;35:*.webp=01;35:*.pdf=00;33:*.ps=00;33:*.txt=00;33:*.md=00;33:*.json=00;33:*.yaml=00;33:*.yml=00;33:*.toml=00;33:*.conf=00;33:*.sh=01;32:*.bash=01;32:*.zsh=01;32:*.py=01;32:*.go=01;32:*.rs=01;32:*.js=01;32:*.ts=01;32",
	)
	if dir != "" {
		cmd.Dir = dir
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, 0, fmt.Errorf("start PTY failed: %w", err)
	}
	return &unixPty{f: ptmx, cmd: cmd}, cmd.Process.Pid, nil
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
