//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// On Linux the page plays the beeps (the program has no sound output).
const nativeSound = false

func attachConsole() {}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "netmon: "+format+"\n", a...)
	os.Exit(1)
}

// lockInstance takes an exclusive, non-blocking flock on path.
func lockInstance(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// runFrontend: Linux is a plain console program. Ctrl+C stops it.
func runFrontend(e *Engine, uiURL string, open bool) {
	fmt.Println("  Press Ctrl+C to stop monitoring.")
	fmt.Println()
	if open {
		openBrowser(uiURL)
	}
	select {}
}
