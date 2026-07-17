//go:build windows

package main

import "os/exec"

func configureChildProcess(_ *exec.Cmd) {}

func signalChildProcess(cmd *exec.Cmd, _ bool) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
