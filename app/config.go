package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const maxUserURLs = 5

type Preset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Presets are always shown first in the URL list. Keep in sync with ping10.htm.
var presets = []Preset{
	{"Google", "https://clients3.google.com/generate_204?r={rnd}"},
	{"Cloudflare", "https://cp.cloudflare.com/generate_204?r={rnd}"},
	{"Apple", "https://captive.apple.com/hotspot-detect.html?r={rnd}"},
	{"Firefox", "https://detectportal.firefox.com/success.txt?r={rnd}"},
	{"Aparat (Iran)", "https://www.aparat.com/robots.txt?r={rnd}"},
}

type Config struct {
	URL         string   `json:"url"`
	Interval    int      `json:"interval"`    // seconds between probes
	DLInterval  int      `json:"dlInterval"`  // minutes, 0 = off
	ULInterval  int      `json:"ulInterval"`  // minutes, 0 = off
	SpeedServer string   `json:"speedServer"` // "ookla" | "cloudflare"
	UserURLs    []string `json:"userUrls"`    // max 5, oldest first
	SoundMode   string   `json:"soundMode"`   // "off" | "on" | "err"
}

func defaultConfig() Config {
	return Config{
		URL:         presets[0].URL,
		Interval:    3,
		SpeedServer: "ookla",
		UserURLs:    []string{},
		SoundMode:   "off",
	}
}

func (c *Config) normalize() {
	d := defaultConfig()
	if c.URL == "" {
		c.URL = d.URL
	}
	if c.Interval < 1 {
		c.Interval = 1
	}
	if c.Interval > 300 {
		c.Interval = 300
	}
	if c.DLInterval < 0 {
		c.DLInterval = 0
	}
	if c.ULInterval < 0 {
		c.ULInterval = 0
	}
	if c.SpeedServer != "cloudflare" {
		c.SpeedServer = "ookla"
	}
	if c.SoundMode != "on" && c.SoundMode != "err" {
		c.SoundMode = "off"
	}
	if c.UserURLs == nil {
		c.UserURLs = []string{}
	}
	if len(c.UserURLs) > maxUserURLs {
		c.UserURLs = c.UserURLs[len(c.UserURLs)-maxUserURLs:]
	}
}

func configPath(dataDir string) string { return filepath.Join(dataDir, "config.json") }

func loadConfig(dataDir string) Config {
	c := defaultConfig()
	if b, err := os.ReadFile(configPath(dataDir)); err == nil {
		if json.Unmarshal(b, &c) != nil {
			c = defaultConfig()
		}
	}
	c.normalize()
	return c
}

func saveConfig(dataDir string, c Config) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return writeFileAtomic(configPath(dataDir), b)
}

func isPreset(u string) bool {
	for _, p := range presets {
		if p.URL == u {
			return true
		}
	}
	return false
}

// addUserURL appends u (moving it to the end if it is already there) and keeps the last 5.
func addUserURL(list []string, u string) []string {
	out := make([]string, 0, maxUserURLs+1)
	for _, x := range list {
		if x != u {
			out = append(out, x)
		}
	}
	out = append(out, u)
	if len(out) > maxUserURLs {
		out = out[len(out)-maxUserURLs:]
	}
	return out
}
