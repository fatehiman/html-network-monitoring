//go:build windows

package sound

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pPlaySound = windows.NewLazySystemDLL("winmm.dll").NewProc("PlaySoundW")
	cacheMu    sync.Mutex
	cache      = map[[2]int][]byte{} // built once per (freq, ms)
)

// Play plays a beep and returns when it has finished.
func Play(freq, ms int) {
	cacheMu.Lock()
	w, ok := cache[[2]int{freq, ms}]
	if !ok {
		w = WAV(float64(freq), ms)
		cache[[2]int{freq, ms}] = w
	}
	cacheMu.Unlock()
	const sndSync, sndNoDefault, sndMemory = 0x0, 0x2, 0x4
	pPlaySound.Call(uintptr(unsafe.Pointer(&w[0])), 0, sndMemory|sndSync|sndNoDefault)
}
