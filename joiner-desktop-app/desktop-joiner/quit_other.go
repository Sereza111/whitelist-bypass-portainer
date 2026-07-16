//go:build !linux

package main

import (
	"bufio"
	"os"
	"strings"
)

func watchStdinQuit(sig chan<- os.Signal) {
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			if strings.EqualFold(strings.TrimSpace(scanner.Text()), "QUIT") {
				sig <- os.Interrupt
				return
			}
		}
		// Electron closes stdin during graceful shutdown.
		sig <- os.Interrupt
	}()
}
