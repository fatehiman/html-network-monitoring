// netmon — native HTTPS response-time and speed monitor.
//
// All measuring runs inside this program, so it never slows down or stops the
// way browser timers do in hidden tabs. The UI is a web page served on
// localhost; it only shows data and can be closed at any time.
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
	"time"
)

var version = "dev" // set by build: -ldflags "-X main.version=..."

const userAgent = "Mozilla/5.0 (netmon) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"

var quiet bool

//go:embed web
var webFS embed.FS

func main() {
	listen := flag.String("listen", "127.0.0.1", "address to listen on (use 0.0.0.0 to allow other PCs on the LAN)")
	port := flag.Int("port", 8765, "HTTP port of the UI")
	dataDir := flag.String("data", defaultDataDir(), "folder for config and measurement data")
	noBrowser := flag.Bool("no-browser", false, "do not open the UI in a browser at start")
	flag.BoolVar(&quiet, "quiet", false, "do not print the log to the console")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVer {
		fmt.Println("netmon", version)
		return
	}

	addr := net.JoinHostPort(*listen, fmt.Sprint(*port))
	uiURL := fmt.Sprintf("http://%s/", net.JoinHostPort(browseHost(*listen), fmt.Sprint(*port)))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Probably already running: just show the existing instance.
		if isNetmon(uiURL) {
			fmt.Println("netmon is already running at", uiURL)
			if !*noBrowser {
				openBrowser(uiURL)
			}
			return
		}
		log.Fatalf("cannot listen on %s: %v", addr, err)
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("data folder: %v", err)
	}
	eng, err := newEngine(*dataDir)
	if err != nil {
		log.Fatalf("init: %v", err)
	}

	fmt.Printf("netmon %s\n  UI:   %s\n  data: %s\n  Close this window (or Ctrl+C) to stop monitoring.\n\n", version, uiURL, *dataDir)
	eng.Start()
	if !*noBrowser {
		go func() { time.Sleep(300 * time.Millisecond); openBrowser(uiURL) }()
	}
	log.Fatal(http.Serve(ln, newMux(eng)))
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

func isNetmon(url string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url + "api/ping")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.Header.Get("X-Netmon") != ""
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
