//go:build windows

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/UserExistsError/conpty"
)

// windowsPty Windows PTY implementation based on ConPTY
type windowsPty struct {
	c *conpty.ConPty
}

func (p *windowsPty) Read(b []byte) (int, error)  { return p.c.Read(b) }
func (p *windowsPty) Write(b []byte) (int, error) { return p.c.Write(b) }
func (p *windowsPty) Close() error                { return p.c.Close() }
func (p *windowsPty) Resize(cols, rows int) error  { return p.c.Resize(cols, rows) }
func (p *windowsPty) Wait() error {
	_, err := p.c.Wait(context.Background())
	return err
}

func startPty(shellPath, dir string, cols, rows int) (ptyConn, int, error) {
	var opts []conpty.ConPtyOption
	opts = append(opts, conpty.ConPtyDimensions(cols, rows))
	if dir != "" {
		opts = append(opts, conpty.ConPtyWorkDir(dir))
	}
	opts = append(opts, conpty.ConPtyEnv(append(os.Environ(), "TERM=xterm-256color")))

	cpty, err := conpty.Start(shellPath, opts...)
	if err != nil {
		return nil, 0, fmt.Errorf("start PTY failed: %w", err)
	}
	return &windowsPty{c: cpty}, cpty.Pid(), nil
}

func getExitCode(err error) int {
	if err == nil {
		return 0
	}
	return 1
}
