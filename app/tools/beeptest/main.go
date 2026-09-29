//go:build windows

// Command beeptest plays the tray app's "On" sound pattern, to check the beeps by ear.
package main

import (
	"time"

	"netmon/internal/sound"
)

func main() {
	for i := 0; i < 3; i++ {
		sound.Play(300, 50) // request sent
		time.Sleep(120 * time.Millisecond)
		sound.Play(250, 50) // reply < 3 s
		time.Sleep(700 * time.Millisecond)
	}
	sound.Play(1000, 50) // reply >= 3 s or failed
}
