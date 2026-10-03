# HTTPS Response Time Monitor (netmon)

A tool to watch your internet connection. It sends a small HTTPS request to a target URL every few seconds, measures the round-trip time, and draws it on a live chart. It can also measure download and upload speed on a schedule.

There are two versions:

| | **netmon app** (recommended) | **Browser version** (`ping10.htm`) |
|---|---|---|
| Runs as | One executable: Windows **system-tray** app, Linux console program | One HTML file opened in a browser |
| Keeps measuring when the window is hidden / minimized / closed | **Yes** — measuring runs in the program, not in the page | No — browsers slow down and then stop timers in hidden tabs |
| Speed test server | Nearest **Ookla** (speedtest.net) server, or Cloudflare | Cloudflare only (Ookla servers block browser requests with CORS) |
| Data saved in | Files in a data folder | Browser `localStorage` |
| Works on a Linux server with no screen | Yes | No |

Older browser versions (`ping7.htm` … `ping9.htm`) are kept for reference.

---

## netmon app

### Download and run

Get the file for your system from the [GitHub Releases](https://github.com/fatehiman/html-network-monitoring/releases) page:

| System | File |
|---|---|
| Windows 64-bit (most PCs) | `netmon-windows-amd64.exe` |
| Windows on ARM | `netmon-windows-arm64.exe` |
| Linux 64-bit (PC / server) | `netmon-linux-amd64` |
| Linux ARM 64-bit (Raspberry Pi 4/5 with 64-bit OS) | `netmon-linux-arm64` |
| Linux ARM 32-bit (older Raspberry Pi) | `netmon-linux-arm` |

Nothing to install. No browser engine is inside the program.

- **Windows:** double-click the `.exe`. There is **no window**: netmon runs in the **system tray** (see below). Windows 11 hides new tray icons under the **^** arrow; drag the icon to the taskbar to keep it visible.
- **Linux:** a console program: `chmod +x netmon-linux-amd64 && ./netmon-linux-amd64`. It opens the UI in a browser if there is a desktop. Press `Ctrl+C` to stop.

The UI is at **http://127.0.0.1:8765**. You can close the browser tab at any time — measuring does not stop.

**Only one copy runs at a time** (per data folder). If you start netmon again while it is running, it shows an error (a message box on Windows, a message on the console on Linux) with the address of the running copy, and exits.

**Busy port:** if port 8765 is used by another program, netmon tries 8766, 8767, … (up to 100 ports) and uses the first free one. The tray **Open** item, the console output and the error message always show the real address.

### Windows tray icon

The icon is a pulse line on a colored square. The color follows the **last** reply, with the same colors as the dots on the chart:

| Color | Meaning |
|---|---|
| 🟩 green | reply in less than 500 ms |
| 🟧 orange | reply in 500–1999 ms |
| 🟥 red | reply in 2000 ms or more, or no reply (timeout 5 s / error) |
| ⬜ grey | starting, or paused |

Hover over it to see the last time, the target host and the last DL/UL speed.

Right-click menu:

- **Open** — opens the UI in your default browser
- **Sound** ▸ **Off** / **On** / **Err** — only one is checked:
  - **Off** — no sound
  - **On** — 300 Hz beep when a request is sent; on reply 250 Hz (< 3000 ms) or 1000 Hz (≥ 3000 ms or failed); each 50 ms
  - **Err** — only the 1000 Hz beep, for replies ≥ 3000 ms or failed
- **Exit** — stops monitoring and closes netmon

The sound choice is saved and used on the next start. The Off/On/Err buttons in the page change the same setting. On Windows the **program** plays the beeps (smooth sine tones, see below), so they work with no browser open, and the page stays silent to avoid double beeps.

### Command-line options

| Option | Default | Meaning |
|---|---|---|
| `-port` | `8765` | Port of the UI. If it is busy, the next free port is used |
| `-listen` | `127.0.0.1` | Address to listen on. Use `0.0.0.0` to open the UI from other PCs on your LAN (anyone on the LAN can then control it) |
| `-data` | Windows: `%AppData%\netmon`, Linux: `~/.config/netmon` | Folder for config and measurements. One netmon can run per folder |
| `-no-browser` | off | Linux: do not open the browser at start (Windows never opens it at start) |
| `-quiet` | off | Do not print the log to the console |
| `-version` | | Print the version and exit |

On Windows the options work too (for example in a shortcut). If you start the `.exe` from a terminal, its output is printed there.

### Run all the time

**Linux (systemd)** — `/etc/systemd/system/netmon.service`:

```ini
[Unit]
Description=netmon internet monitor
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/netmon-linux-amd64 -no-browser -quiet -listen 0.0.0.0 -data /var/lib/netmon
Restart=always
DynamicUser=yes
StateDirectory=netmon

[Install]
WantedBy=multi-user.target
```

Then `sudo systemctl enable --now netmon` and open `http://<server-ip>:8765`.

**Windows** — start it at logon: press `Win+R`, type `shell:startup`, and put a shortcut to `netmon-windows-amd64.exe` in that folder.

### How it works

- **Engine (Go):** a loop sends the probe, waits for the answer, stores the point, then waits `interval` seconds and repeats (the wait starts *after* the answer, so slow answers never pile up). Go timers are not slowed down when the window is hidden.
- **Probe:** `GET` on the URL, `{rnd}` is replaced by a random 8-digit number (cache busting). Any HTTP answer (even 403/404) counts as success, because the goal is the network round-trip. No answer within 5 s = failure. Redirects are not followed. Connections are reused (keep-alive), like a browser does.
- **UI:** the page in `app/web/index.html` is built into the executable. It loads the data with `GET /api/state` and `GET /api/range?key=…`, then gets live updates as Server-Sent Events from `/api/events`. Buttons call `POST /api/…`. POST requests must have the header `X-Netmon: 1`, so other web sites cannot control netmon from your browser.
- **Tray (Windows):** `tray_windows.go` uses `fyne.io/systray` (pure Go on Windows) and listens to the same live events as the page. The icons are drawn in code (`internal/icon`).
- **Beeps:** Windows — played by the program (`internal/sound`): a sine tone with a 5 ms fade-in and fade-out, played with `PlaySound`. The Windows `Beep()` API is not used, because it makes a square wave that starts and stops at full volume, which sounds like a click or buzz. Linux — played by the page while a UI tab is open (click once on the page to allow audio).
- **Single instance:** an exclusive lock on `netmon.lock` in the data folder (`LockFileEx` on Windows, `flock` on Linux). The OS releases it when the program ends, even after a crash. The file holds the UI address for the error message.

### Data files

In the data folder:

- `config.json` — target URL, intervals, speed server, your saved URLs, sound mode, auto-tag-by-IP checkbox
- `ranges/YYYYMMDD-HH.json` — one file per 2-hour range: `{"points": [[timestampMs, ms|null], …], "tags": […], "speed": […]}`
- `netmon.lock` — single-instance lock

Ranges older than 10 days are deleted; at most 120 ranges are kept.

### API

| Method | Path | Body | Result |
|---|---|---|---|
| GET | `/api/state` | | config, presets, running, live range key, range list, last 200 log lines |
| GET | `/api/range?key=2026/03/06 00:00-02:00` | | points, tags, speed results of one range |
| GET | `/api/events` | | Server-Sent Events: `probe`, `point`, `range`, `log`, `tags`, `speed`, `speedstate`, `speedprogress`, `config`, `running`, `cleared`, `ip` |
| POST | `/api/config` | any of `url`, `interval`, `dlInterval`, `ulInterval`, `speedServer`, `soundMode`, `autoTagIp` | new config |
| POST | `/api/urls/add` / `/api/urls/remove` | `{"url": "…"}` | new config |
| POST | `/api/toggle` | | `true` = running |
| POST | `/api/tag` | `{"text": "4G"}` | |
| POST | `/api/tag/delete` | `{"key": "…", "idx": 0}` | |
| POST | `/api/speed` | `{"type": "dl"}` or `"ul"` | `false` if that test is already queued |
| POST | `/api/clear` | | deletes all data |

### Build from source

Needs Go 1.22 or newer. No C compiler is needed (pure Go, `CGO_ENABLED=0`), so all targets build from any OS.

```sh
cd app
go test ./...
./build.sh 1.0.0        # or on Windows: .\build.ps1 -Version 1.0.0
```

The executables go to `dist/`.

Windows executables are built with `-H windowsgui` (no console window).

Source files in `app/`:

| File | What |
|---|---|
| `main.go` | flags, single instance, free port, HTTP server |
| `engine.go` | probe loop, ranges, tags, events |
| `speed.go` | speed tests |
| `store.go` | data files |
| `config.go` | config and URL presets |
| `tray_windows.go` | Windows tray, beeps, message box, lock |
| `platform_other.go` | Linux console front end, lock |
| `internal/icon/` | draws the icon (tray, favicon, exe) |
| `internal/sound/` | makes and plays the Windows beeps |
| `web/index.html` | UI |
| `rsrc_windows_*.syso` | exe icon + manifest, made with go-winres (see below) |

To change the exe icon, edit `internal/icon`, then:

```sh
go run ./tools/genicon winres/icon.png
go run github.com/tc-hib/go-winres@v0.3.3 simply --icon winres/icon.png --manifest gui --product-name netmon --file-description "netmon internet monitor" --arch amd64,arm64
```

`go run ./tools/iconpreview preview.png` draws all colors and sizes on one sheet. `go run ./tools/beeptest` (Windows) plays the "On" beep pattern.

---

## Features (both versions)

### 1. Ping monitor

- **Chart:** X = time (one 2-hour range), Y = response time (0–5000 ms)
- **Points:** green (<500 ms), yellow (<2000 ms), red (≥2000 ms); failed requests are red dots at the top
- **Stats:** Last, Average, Min, Max, Requests, Failures, Jitter (average difference between neighbour samples, last 40). On the live range, stats start at the active tag, so each network gets its own numbers.

### 2. Target URL list

Click **▾** next to the URL box to choose a URL:

| Preset | URL | Why |
|---|---|---|
| Google | `https://clients3.google.com/generate_204?r={rnd}` | Android's own connectivity check (empty 204 answer) |
| Cloudflare | `https://cp.cloudflare.com/generate_204?r={rnd}` | Cloudflare's connectivity check (empty 204 answer) |
| Apple | `https://captive.apple.com/hotspot-detect.html?r={rnd}` | iPhone/Mac connectivity check (tiny page) |
| Firefox | `https://detectportal.firefox.com/success.txt?r={rnd}` | Firefox connectivity check (tiny text) |
| Aparat (Iran) | `https://www.aparat.com/robots.txt?r={rnd}` | Large Iranian site hosted **inside Iran**. When international internet is cut but the national network works, this one keeps answering while the others fail — so you can see which kind of outage it is |

**Your own URLs:** type a URL and click **+**. It is added under "My URLs" and becomes the target. Only the **last 5** are kept: adding a 6th removes the oldest. Adding a URL that is already in the list moves it to the end. Click **✕** to remove one. The list order is always: 5 presets, then up to 5 of your URLs.

Put `{rnd}` in your URL to avoid cached answers.

### 3. Audio beeps

Three modes:

- **Off** — silent
- **On** — 300 Hz when a request is sent, 250 Hz when the answer is <3000 ms, 1000 Hz when ≥3000 ms or failed
- **Err** — only the 1000 Hz beep for slow (≥3000 ms) or failed requests

Browser version: not saved, always starts Off. App: saved in `config.json`; on Windows shared with the tray menu.

Beeps are dropped (not queued) while the browser's `AudioContext` is suspended.

### 4. Network tags

- Type a label (e.g. "WiFi-Home", "4G") and press Enter or ⏎. A colored bar appears above the chart.
- The color comes from the text (same text = same color).
- The active tag grows as time passes (at least 60 px wide). Adding a new tag closes the previous one.
- Right-click a tag to delete it.
- At a range boundary, the active tag is closed and continued in the new range.
- **Exit IP:** the program asks `https://api.ipify.org/` for your public IP every 10 minutes and shows it in parentheses next to the "Auto tag" checkbox; hover over it to see when it was last checked.
- **Auto tag (IP):** check the box to tag the chart with your exit IP automatically, exactly as if you typed it and pressed Enter. Whenever the IP changes, a new tag is added. The checkbox is saved in `config.json` and remembered on the next run (default: unchecked).

### 5. Speed tests (download and upload)

Why the old version showed much lower speeds than speedtest.net:

1. **One connection.** One TCP connection often cannot fill a fast line, especially when latency is high. speedtest.net uses several at the same time.
2. **Tiny, fixed size (5 MB).** The time included DNS, TCP and TLS setup and TCP slow-start, which are a big part of a short transfer.
3. **Far server (native app only).** speedtest.net uses a server near you (for example inside Iran). Cloudflare's route can go abroad and be slower.

How it works now:

- **Download:** 6 parallel connections for 10 s. The first 2 s are not counted (warm-up). Bytes are counted as they arrive.
- **Upload:** 4 parallel connections for 10 s. Only requests that the **server confirmed** are counted (counting bytes as they are written would be wrong, because the OS send buffer takes several MB at once). Each connection doubles its request size (256 KB → up to 64 MB) until one request takes at least 0.5 s. The first 2 s are not counted.
- **Server (app):** *Ookla (nearest)* asks speedtest.net for servers near your IP, measures latency to each and uses the fastest one (kept for 30 minutes). *Cloudflare* uses `speed.cloudflare.com`. If the Ookla server list fails, Cloudflare is used.
- **Server (browser):** Cloudflare only.
- DL and UL never run at the same time (they would share the line). Manual and scheduled tests go through one queue.
- The button shows the live speed while a test runs. Results are shown under the chart (DL blue, UL purple, ✗ = failed). If two tests of the same type are within 60 s, the newer one replaces the older one.
- **Data use:** one test moves about `speed × 10 s` of data (≈125 MB at 100 Mbps). Be careful with short auto-test intervals on mobile data.

**Scheduled tests:** set minutes in the box next to ⬇DL / ⬆UL (0 = off).

### 6. Other controls

- **⏸ / ▶** pauses or resumes the ping loop (speed tests are independent).
- **Clear** (with confirm) deletes all data, tags and speed results.
- **Range** list shows every 2-hour range that has data. When the live range ends and you are looking at it, the view moves to the new range.

---

## Browser version details (`ping10.htm`)

**Single file**: HTML, CSS and JS in one file, no dependencies, no build. Works from `file:///` in Chrome/Edge/Firefox.

**Important:** keep the tab visible. Browsers slow down timers in hidden tabs and later stop them. Use the netmon app if you need non-stop monitoring.

**CORS strategy**: the probe uses `fetch(url, { mode: 'no-cors' })`. The response cannot be read, but the full TCP+TLS+HTTP round-trip still happens, so the time is correct. Timeout 5 s (`AbortController`).

**Why `setTimeout` instead of `setInterval`**: the next request is scheduled after the answer, so a 20-second answer does not cause overlapping requests.

### localStorage keys

| Prefix | Example key | Value | Description |
|---|---|---|---|
| `httpsmon_` | `httpsmon_2026/03/06 00:00-02:00` | `[[timestamp, ms\|null], ...]` | Ping points. `null` = failed |
| `httpsmon_tags_` | `httpsmon_tags_2026/03/06 00:00-02:00` | `[{text, color, start, end}, ...]` | Tags of the range |
| `httpsmon_speed_` | `httpsmon_speed_2026/03/06 00:00-02:00` | `[{time, type, mbps}, ...]` | Speed results (`type`: `"dl"` / `"ul"`) |
| `httpsmon_config` | `httpsmon_config` | `{url, interval, dlInterval, ulInterval, userUrls}` | Saved config |

- Ranges older than 10 days are purged; at most 120 ranges are kept.
- If storage is full, old data is purged before saving config.
- Config is applied **before** event listeners are attached, to avoid save-on-apply loops.

## UI layout

```
┌──────────────────────────────────────────────────────────────────┐
│ 📡 HTTPS Response Time Monitor [⏸] [Clear] 🔊[Off|On|Err] [Range ▼]│
│ [URL input][+][▾] [Interval]        [☐ Auto tag (IP)] [Tag: ___ ⏎] │
│                               [Server ▼][⬇DL][_]min [⬆UL][_]min │
├──────────────────────────────────────────────────────────────────┤
│ Status | Last | Average | Min | Max | Requests | Failures | Jitter│
├──────────────────────────────────────────────────────────────────┤
│ [Tag strip]                                                      │
│ Main chart (Y: 0–5000 ms, X: 2 h range)                          │
│ DL:  45    62    58                                              │
│ UL:  12    15    11                                              │
├──────────────────────────────────────────────────────────────────┤
│ Live log (newest at bottom, max 200 lines)                       │
└──────────────────────────────────────────────────────────────────┘
```

(The server selector exists only in the app.)
