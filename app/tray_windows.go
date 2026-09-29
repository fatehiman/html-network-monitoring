//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"netmon/internal/icon"
	"netmon/internal/sound"
)

// On Windows the program plays the beeps itself (smooth sine tones, see
// internal/sound), so they work with no browser open.
const nativeSound = true

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	pAttachConsole   = kernel32.NewProc("AttachConsole")
	pMessageBox      = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
	consoleAvailable bool
)

// attachConsole: the exe is built as a GUI program (no console window). When it
// is started from a terminal, write output to that terminal.
func attachConsole() {
	const attachParentProcess = ^uintptr(0)
	if r, _, _ := pAttachConsole.Call(attachParentProcess); r == 0 {
		log.SetOutput(io.Discard)
		quiet = true
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
		os.Stdout, os.Stderr = f, f
		log.SetOutput(f)
		consoleAvailable = true
	}
}

func fatal(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if consoleAvailable {
		fmt.Fprintln(os.Stderr, "netmon:", msg)
	}
	const mbIconError, mbSetForeground, mbTopmost = 0x10, 0x10000, 0x40000
	t, _ := windows.UTF16PtrFromString(msg)
	c, _ := windows.UTF16PtrFromString("netmon")
	pMessageBox.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), mbIconError|mbSetForeground|mbTopmost)
	os.Exit(1)
}

// lockInstance locks one byte far past the end of the file, so the file content
// (the running instance's URL) can still be read by a second instance.
func lockInstance(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	ol := &windows.Overlapped{OffsetHigh: 1}
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// ===================== SOUND =====================

var beepCh = make(chan [2]uint32, 8)

func init() {
	go func() {
		for b := range beepCh {
			sound.Play(int(b[0]), int(b[1])) // blocks for the duration
		}
	}()
}

// beep never waits: if beeps pile up, extra ones are dropped (like the page does).
func beep(freq, ms uint32) {
	select {
	case beepCh <- [2]uint32{freq, ms}:
	default:
	}
}

// ===================== TRAY =====================

type tray struct {
	e      *Engine
	uiURL  string
	icons  map[string][]byte
	sounds map[string]*systray.MenuItem

	mu      sync.Mutex
	mode    string // sound mode
	running bool
	color   string
	host    string
	last    string // last probe text for the tooltip
	dl, ul  float64
}

func runFrontend(e *Engine, uiURL string, _ bool) {
	t := &tray{e: e, uiURL: uiURL, icons: map[string][]byte{
		"green":  icon.ICO(icon.Green),
		"orange": icon.ICO(icon.Orange),
		"red":    icon.ICO(icon.Red),
		"grey":   icon.ICO(icon.Grey),
	}}
	systray.Run(t.onReady, func() {})
}

func (t *tray) onReady() {
	st := t.e.State()
	t.mode, t.running, t.host = st.Config.SoundMode, st.Running, hostOf(st.Config.URL)
	t.last = "starting…"
	if rd, ok := t.e.Range(st.LiveKey); ok {
		t.setSpeed(rd.Speed)
	}
	t.color = ""
	t.setColor("grey")

	mOpen := systray.AddMenuItem("Open", "Open the UI in your browser")
	mSound := systray.AddMenuItem("Sound", "Beep on requests / replies")
	t.sounds = map[string]*systray.MenuItem{
		"off": mSound.AddSubMenuItemCheckbox("Off", "No sound", false),
		"on":  mSound.AddSubMenuItemCheckbox("On", "Beep on every request and reply", false),
		"err": mSound.AddSubMenuItemCheckbox("Err", "Beep only on slow (≥ 3 s) or failed replies", false),
	}
	systray.AddSeparator()
	mExit := systray.AddMenuItem("Exit", "Stop monitoring and exit")
	t.showSoundChecks(t.mode)
	t.updateTooltip()

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser(t.uiURL)
			case <-t.sounds["off"].ClickedCh:
				t.setSound("off")
			case <-t.sounds["on"].ClickedCh:
				t.setSound("on")
			case <-t.sounds["err"].ClickedCh:
				t.setSound("err")
			case <-mExit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
	go t.watch()
}

func (t *tray) setSound(m string) {
	t.showSoundChecks(m)
	t.e.UpdateConfig(ConfigPatch{SoundMode: &m}) // saved in config.json; the page shows it too
}

// showSoundChecks makes the three items act like radio buttons.
func (t *tray) showSoundChecks(m string) {
	for k, it := range t.sounds {
		if k == m {
			it.Check()
		} else {
			it.Uncheck()
		}
	}
}

// watch follows the engine's live events (the same ones the page gets).
func (t *tray) watch() {
	ch := t.e.Subscribe()
	for b := range ch {
		var m struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		t.mu.Lock()
		switch m.Type {
		case "probe":
			if t.mode == "on" {
				beep(300, 50)
			}
		case "point":
			var d struct {
				P [2]*int64 `json:"p"`
			}
			json.Unmarshal(m.Data, &d)
			t.onPoint(d.P[1])
		case "running":
			json.Unmarshal(m.Data, &t.running)
			if !t.running {
				t.setColor("grey")
			}
		case "config":
			var c Config
			json.Unmarshal(m.Data, &c)
			if c.SoundMode != t.mode {
				t.mode = c.SoundMode
				t.showSoundChecks(t.mode)
			}
			t.host = hostOf(c.URL)
		case "speed":
			var d struct {
				Speed []SpeedResult `json:"speed"`
			}
			json.Unmarshal(m.Data, &d)
			t.setSpeed(d.Speed)
		case "cleared":
			t.dl, t.ul = 0, 0
		}
		t.updateTooltip()
		t.mu.Unlock()
	}
}

// onPoint: icon color = chart dot color; beeps = the page's rules.
func (t *tray) onPoint(ms *int64) {
	switch {
	case ms == nil:
		t.setColor("red")
		t.last = "no answer"
	case *ms < 500:
		t.setColor("green")
	case *ms < 2000:
		t.setColor("orange")
	default:
		t.setColor("red")
	}
	if ms != nil {
		t.last = fmt.Sprintf("%d ms", *ms)
	}
	slow := ms == nil || *ms >= 3000
	if t.mode == "on" {
		if slow {
			beep(1000, 50)
		} else {
			beep(250, 50)
		}
	} else if t.mode == "err" && slow {
		beep(1000, 50)
	}
}

func (t *tray) setColor(c string) {
	if c == t.color {
		return
	}
	t.color = c
	systray.SetIcon(t.icons[c])
}

func (t *tray) setSpeed(list []SpeedResult) {
	for _, s := range list {
		if s.Type == "dl" {
			t.dl = s.Mbps
		} else {
			t.ul = s.Mbps
		}
	}
}

func (t *tray) updateTooltip() {
	status := t.last
	if !t.running {
		status = "paused"
	}
	tip := "netmon — " + status + "\n" + t.host
	if t.dl > 0 || t.ul > 0 {
		tip += fmt.Sprintf("\nDL %s · UL %s Mbps", fmtMbps(t.dl), fmtMbps(t.ul))
	}
	if r := []rune(tip); len(r) > 120 { // Windows limit is 127 characters
		tip = string(r[:120])
	}
	systray.SetTooltip(tip)
}

func hostOf(u string) string {
	if p, err := url.Parse(strings.ReplaceAll(u, "{rnd}", "0")); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}
