# HTTPS Response Time Monitor

A single-file HTML/JS monitoring tool that measures HTTPS response times by probing a target URL at regular intervals and visualizing the results on a real-time chart. Designed to run locally from disk (`file:///`) in Chrome or any modern browser.

## Architecture Overview

**Single file**: Everything (HTML, CSS, JS) lives in one `.html` file with no dependencies or build steps.

**Core loop**: A recursive `setTimeout` chain (`tick()`) sends a `fetch()` request in `no-cors` mode, measures round-trip time via `performance.now()`, stores the result, and schedules the next tick *after* the response completes (not on a fixed interval clock).

**CORS strategy**: Uses `fetch(url, { mode: 'no-cors' })` which returns an opaque response. The response body/status cannot be read, but the full TCP+TLS+HTTP round-trip still occurs, so elapsed time accurately reflects network latency. A `{rnd}` placeholder in the URL is replaced with a random 8-digit number on each request to prevent caching.

**Timeout**: Each request has a 5-second `AbortController` timeout.

## Data Model

### Time Ranges

All data is organized into **2-hour time ranges** aligned to even hours (00:00-02:00, 02:00-04:00, etc.).

- Range key format: `"2026/03/06 00:00-02:00"`
- When current time crosses a range boundary, a new range is created automatically
- The chart always displays exactly one range at a time
- A dropdown listbox shows all ranges that have stored data (no empty ranges, no future ranges)
- When viewing the previous live range and time crosses into a new range, the view auto-switches to the new range; otherwise the user's selection is preserved

### localStorage Keys

| Prefix | Example Key | Value Format | Description |
|---|---|---|---|
| `httpsmon_` | `httpsmon_2026/03/06 00:00-02:00` | `[[timestamp, ms\|null], ...]` | Ping data points. `null` = failed/timeout |
| `httpsmon_tags_` | `httpsmon_tags_2026/03/06 00:00-02:00` | `[{text, color, start, end}, ...]` | Network tags for the range |
| `httpsmon_speed_` | `httpsmon_speed_2026/03/06 00:00-02:00` | `[{time, type, mbps}, ...]` | Speed test results (`type`: `"dl"` or `"ul"`) |
| `httpsmon_config` | `httpsmon_config` | `{url, interval, dlInterval, ulInterval}` | Saved configuration |

### Storage Management

- Ranges older than **10 days** are automatically purged (data + tags + speed results)
- Maximum **120 ranges** stored (10 days × 12 ranges/day)
- Config is prioritized over data: if storage is full, old data is purged before saving config
- `getAllRangeKeys()` explicitly excludes config, tag, and speed keys to avoid parse errors

## Features

### 1. Ping Monitor (Main Feature)

- **URL**: Configurable, default `https://clients3.google.com/generate_204?r={rnd}`
- **Interval**: Configurable in seconds (default 3), measures time *between responses* not between starts
- **Chart**: Canvas-based, X-axis = time (2h range), Y-axis = response time (0-5000ms)
- **Data points**: Green (<500ms), yellow (<2000ms), red (≥2000ms); failed requests shown as red dots at top
- **Line + gradient fill** connecting successful data points
- **Auto-start**: Monitoring begins immediately on page load

### 2. Audio Beeps

- **Muted by default** (always starts muted on page load, not saved in config)
- Toggle via slide switch in the UI
- Request sent: 300 Hz, 50ms
- Response < 3000ms: 250 Hz, 50ms
- Response ≥ 3000ms: 1000 Hz, 50ms
- Failure/timeout: 1000 Hz, 50ms
- Beeps are silently dropped when `AudioContext` is suspended (prevents buffering/queuing)

### 3. Network Tags

- User types a label (e.g., "WiFi-Home", "4G") and presses Enter or clicks ⏎
- A colored bar appears in a **dedicated strip above the chart**
- Color is randomly generated per tag (`hsla(hue, 70%, 55%, 0.45)`)
- The active tag **expands in width** as time passes (minimum 60px while active)
- Adding a new tag **closes the previous one** (no overlap)
- Closed tags smaller than 5px are hidden; text is clipped (no wrapping)
- **Right-click** a tag to delete it (no confirmation)
- Tags persist in localStorage per range
- When a range boundary is crossed, the active tag is closed at the boundary and **continued into the new range** with the same text and color

### 4. Speed Tests (Download & Upload)

- **Download**: Fetches 5MB from `https://speed.cloudflare.com/__down?bytes=5242880`
- **Upload**: POSTs a 5MB blob to `https://speed.cloudflare.com/__up`
- Both use standard `fetch()` (Cloudflare supports CORS natively)
- Results displayed as **Mbps numbers below the chart** in two rows: DL (blue) and UL (purple)
- Values are rounded up (`Math.ceil`)
- Failed tests show ✗ in red
- If two tests of the same type occur within 60 seconds, the previous result is replaced

**Manual tests**: Click ⬇DL or ⬆UL buttons. Can run simultaneously.

**Scheduled tests**: Set interval in minutes per textbox (0 = disabled). Scheduled DL and UL tests are **sequential** (never overlap) — they go through a queue (`speedQueue`). After DL finishes, UL starts if queued.

### 5. Configuration Persistence

Saved to localStorage on every input change:
- Target URL
- Ping interval (seconds)
- DL test interval (minutes)
- UL test interval (minutes)

**Not saved**: Mute state (always starts muted).

Config is loaded and applied **before** event listeners are attached to prevent save-on-apply loops.

### 6. Play/Pause

Single toggle button: ⏸ (running) / ▶ (paused). Monitoring auto-starts on page load. Pausing stops the ping loop; speed tests and their schedules are independent.

### 7. Clear All

Requires `confirm()`. Deletes all localStorage keys matching any of the prefixes (data, tags, speed, config).

## UI Layout

```
┌─────────────────────────────────────────────────────────┐
│ 📡 HTTPS Response Time Monitor                          │
│ subtitle                                                │
├─────────────────────────────────────────────────────────┤
│ [URL input] [Interval] [⏸] [Clear] [🔇 Muted]  [Range ▼]│
│                                                 [Tag: ⏎]│
│                                           [⬇DL _min ⬆UL _min]│
├─────────────────────────────────────────────────────────┤
│ Status | Last RTT | Average | Min | Max | Requests | Fails │
├─────────────────────────────────────────────────────────┤
│ [Tag strip - colored bars above chart]                  │
│ ┌─────────────────────────────────────────────────────┐ │
│ │                                                     │ │
│ │              Main Chart (400px)                     │ │
│ │              Y: 0-5000ms, X: 2h range               │ │
│ │                                                     │ │
│ ├─────────────────────────────────────────────────────┤ │
│ │ DL:  45    62    58                    (Mbps row)   │ │
│ │ UL:  12    15    11                    (Mbps row)   │ │
│ └─────────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────────┤
│ [Live log - newest at bottom, auto-scroll, max 200]     │
└─────────────────────────────────────────────────────────┘
```

## Canvas Dimensions

- Main chart area: 400px height
- Speed rows: 4px gap + 16px DL row + 16px UL row = 36px
- Total canvas height: 436px
- Left padding: 60px (Y-axis labels)
- Right padding: 20px
- High-DPI aware: uses `devicePixelRatio` for sharp rendering

## Key Implementation Details

### Why `no-cors` mode?
Standard CORS would require the target server to send `Access-Control-Allow-Origin` headers. Most servers don't. `no-cors` mode allows the fetch to complete (returning an opaque response with status 0) while still performing the full network round-trip needed for timing.

### Why `setTimeout` instead of `setInterval`?
The interval is measured *after* the response, not from request start. If a response takes 20 seconds, the next request fires 3 seconds after that response, not concurrently. This prevents request pileup on slow connections.

### Cache busting
The `{rnd}` placeholder is replaced with `Math.floor(10000000 + Math.random() * 90000000)` — an 8-digit random number ensuring each request URL is unique.

### Audio buffering prevention
If `AudioContext` is suspended (browser policy before user gesture), beeps are silently dropped rather than queued. This prevents a burst of buffered sounds when the user finally clicks.

## Browser Requirements

- Modern browser with `fetch`, `AbortController`, `AudioContext`, `Canvas 2D`, `localStorage`
- Tested in Chrome. Works from `file:///` (local disk)
- No external dependencies or CDN imports