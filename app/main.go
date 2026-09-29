// netmon — native HTTPS response-time and speed monitor.
//
// All measuring runs inside this program, so it never slows down or stops the
// way browser timers do in hidden tabs. The UI is a web page served on
// localhost; it only shows data and can be closed at any time.
// On Windows the program lives in the system tray (tray_windows.go).
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"netmon/internal/icon"
)

var version = "dev" // set by build: -ldflags "-X main.version=..."

const userAgent = "Mozilla/5.0 (netmon) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"

var quiet bool

// instanceLock must stay referenced for the whole run: if it were garbage-collected,
// the file would be closed and the single-instance lock released.
var instanceLock *os.File

//go:embed web
var webFS embed.FS

func main() {
	attachConsole() // Windows GUI build: print to the terminal if started from one
	listen := flag.String("listen", "127.0.0.1", "address to listen on (use 0.0.0.0 to allow other PCs on the LAN)")
	port := flag.Int("port", 8765, "HTTP port of the UI; if it is used by another program, the next free port is used")
	dataDir := flag.String("data", defaultDataDir(), "folder for config and measurement data")
	noBrowser := flag.Bool("no-browser", false, "Linux: do not open the UI in a browser at start")
	flag.BoolVar(&quiet, "quiet", false, "do not print the log to the console")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVer {
		fmt.Println("netmon", version)
		return
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		fatal("Cannot create the data folder:\n%s\n\n%v", *dataDir, err)
	}
	// Only one netmon per data folder. The lock is released by the OS when the process ends.
	var err error
	instanceLock, err = lockInstance(filepath.Join(*dataDir, "netmon.lock"))
	if err != nil {
		msg := "netmon is already running."
		if u := readRunningURL(filepath.Join(*dataDir, "netmon.lock")); u != "" {
			msg += "\n\nIts UI is at " + u
		}
		fatal("%s", msg)
	}

	ln, gotPort, err := listenFree(*listen, *port, 100)
	if err != nil {
		fatal("Cannot open a port for the UI: %v", err)
	}
	uiURL := fmt.Sprintf("http://%s/", net.JoinHostPort(browseHost(*listen), fmt.Sprint(gotPort)))
	instanceLock.Truncate(0)
	instanceLock.WriteAt([]byte(uiURL), 0)

	eng, err := newEngine(*dataDir)
	if err != nil {
		fatal("Cannot start: %v", err)
	}
	if gotPort != *port {
		log.Printf("port %d is used by another program — using %d", *port, gotPort)
	}
	fmt.Printf("netmon %s\n  UI:   %s\n  data: %s\n\n", version, uiURL, *dataDir)
	eng.Start()
	go func() { log.Fatal(http.Serve(ln, newMux(eng))) }()

	runFrontend(eng, uiURL, !*noBrowser) // tray on Windows, console on Linux; returns on exit
}

// listenFree tries port, port+1, ... and returns the first one that is free.
func listenFree(host string, port, tries int) (net.Listener, int, error) {
	var lastErr error
	for p := port; p < port+tries && p <= 65535; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(p)))
		if err == nil {
			return ln, p, nil
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

func readRunningURL(lockPath string) string {
	b, err := os.ReadFile(lockPath)
	if err != nil || !strings.HasPrefix(string(b), "http") {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "netmon")
	}
	return "netmon-data"
}

func browseHost(listen string) string {
	if listen == "" || listen == "0.0.0.0" || listen == "::" {
		return "127.0.0.1"
	}
	return listen
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return // headless Linux: nothing to open
		}
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

// ===================== HTTP =====================

func newMux(e *Engine) http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	favicon := icon.ICO(icon.Green, 16, 32, 48)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		w.Write(favicon)
	})
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, e.State()) })
	mux.HandleFunc("GET /api/range", func(w http.ResponseWriter, r *http.Request) {
		rd, ok := e.Range(r.URL.Query().Get("key"))
		if !ok {
			http.Error(w, "bad key", 400)
			return
		}
		writeJSON(w, rd)
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) { serveEvents(e, w, r) })

	post := func(path string, h func(body json.RawMessage) (any, error)) {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			// A custom header cannot be sent cross-site without a CORS preflight,
			// which we never allow — so other web sites cannot control netmon.
			if r.Header.Get("X-Netmon") != "1" {
				http.Error(w, "forbidden", 403)
				return
			}
			var body json.RawMessage
			json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body)
			res, err := h(body)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			writeJSON(w, res)
		})
	}
	post("/api/config", func(b json.RawMessage) (any, error) {
		var p ConfigPatch
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		return e.UpdateConfig(p), nil
	})
	post("/api/urls/add", func(b json.RawMessage) (any, error) {
		var p struct{ URL string }
		json.Unmarshal(b, &p)
		return e.AddUserURL(p.URL), nil
	})
	post("/api/urls/remove", func(b json.RawMessage) (any, error) {
		var p struct{ URL string }
		json.Unmarshal(b, &p)
		return e.RemoveUserURL(p.URL), nil
	})
	post("/api/toggle", func(json.RawMessage) (any, error) { return e.Toggle(), nil })
	post("/api/clear", func(json.RawMessage) (any, error) { e.ClearAll(); return true, nil })
	post("/api/tag", func(b json.RawMessage) (any, error) {
		var p struct{ Text string }
		json.Unmarshal(b, &p)
		return true, e.AddTag(p.Text)
	})
	post("/api/tag/delete", func(b json.RawMessage) (any, error) {
		var p struct {
			Key string
			Idx int
		}
		json.Unmarshal(b, &p)
		e.RemoveTag(p.Key, p.Idx)
		return true, nil
	})
	post("/api/speed", func(b json.RawMessage) (any, error) {
		var p struct{ Type string }
		json.Unmarshal(b, &p)
		return e.EnqueueSpeed(p.Type), nil
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Netmon", version)
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// serveEvents streams live events (Server-Sent Events) to one UI.
func serveEvents(e *Engine, w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	ch := e.Subscribe()
	defer e.Unsubscribe(ch)
	fmt.Fprint(w, ": hello\n\n")
	fl.Flush()
	keep := time.NewTicker(20 * time.Second)
	defer keep.Stop()
	for {
		select {
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-keep.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
