package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ===================== NOTES =====================
//
// User notes, saved in <data>/notes.json. Each note has its own date-time
// (default: now, editable), so it can be shown on the chart. Notes older than
// keepDays (by that date-time) are deleted, like the ranges.

const (
	maxNoteTitle = 200
	maxNoteTag   = 100
	maxNoteBody  = 10000
)

type Note struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Time    int64  `json:"time"` // ms, the date-time the note is about
	Tag     string `json:"tag"`
	Body    string `json:"body"`
	Created int64  `json:"created"`
	Updated int64  `json:"updated"`
}

func notesPath(dataDir string) string { return filepath.Join(dataDir, "notes.json") }

func loadNotes(dataDir string) []Note {
	var n []Note
	if b, err := os.ReadFile(notesPath(dataDir)); err == nil {
		json.Unmarshal(b, &n)
	}
	if n == nil {
		n = []Note{}
	}
	return n
}

func (e *Engine) saveNotesLocked() {
	b, _ := json.Marshal(e.notes)
	if err := writeFileAtomic(notesPath(e.dataDir), b); err != nil {
		log.Println("save notes:", err)
	}
}

// notesLocked returns a copy, newest first. Caller holds e.mu.
func (e *Engine) notesLocked() []Note {
	out := append([]Note{}, e.notes...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Time != out[j].Time {
			return out[i].Time > out[j].Time
		}
		return out[i].Updated > out[j].Updated
	})
	return out
}

func (e *Engine) Notes() []Note {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.notesLocked()
}

// SaveNote creates the note (empty ID) or updates the note with that ID.
func (e *Engine) SaveNote(n Note) ([]Note, error) {
	n.Title = strings.TrimSpace(n.Title)
	n.Tag = strings.TrimSpace(n.Tag)
	n.Body = strings.TrimRight(n.Body, " \t\r\n")
	switch {
	case n.Title == "":
		return nil, fmt.Errorf("title is empty")
	case utf8.RuneCountInString(n.Title) > maxNoteTitle:
		return nil, fmt.Errorf("title is longer than %d characters", maxNoteTitle)
	case utf8.RuneCountInString(n.Tag) > maxNoteTag:
		return nil, fmt.Errorf("tag is longer than %d characters", maxNoteTag)
	case utf8.RuneCountInString(n.Body) > maxNoteBody:
		return nil, fmt.Errorf("text is longer than %d characters", maxNoteBody)
	case n.Time <= 0:
		return nil, fmt.Errorf("bad date-time")
	case time.UnixMilli(n.Time).Before(time.Now().Add(-keepDays * 24 * time.Hour)):
		return nil, fmt.Errorf("date is older than %d days (it would be deleted)", keepDays)
	}
	now := time.Now().UnixMilli()
	e.mu.Lock()
	defer e.mu.Unlock()
	if n.ID == "" {
		n.ID = newNoteID()
		n.Created, n.Updated = now, now
		e.notes = append(e.notes, n)
	} else {
		i := e.noteIndexLocked(n.ID)
		if i < 0 {
			return nil, fmt.Errorf("note not found (deleted?)")
		}
		n.Created, n.Updated = e.notes[i].Created, now
		e.notes[i] = n
	}
	e.saveNotesLocked()
	out := e.notesLocked()
	e.publishLocked("notes", out)
	return out, nil
}

func (e *Engine) DeleteNote(id string) []Note {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i := e.noteIndexLocked(id); i >= 0 {
		e.notes = append(e.notes[:i], e.notes[i+1:]...)
		e.saveNotesLocked()
	}
	out := e.notesLocked()
	e.publishLocked("notes", out)
	return out
}

func (e *Engine) noteIndexLocked(id string) int {
	for i, n := range e.notes {
		if n.ID == id {
			return i
		}
	}
	return -1
}

// purgeNotesLocked deletes notes older than keepDays. Caller holds e.mu.
func (e *Engine) purgeNotesLocked() {
	cut := time.Now().Add(-keepDays * 24 * time.Hour).UnixMilli()
	out := e.notes[:0]
	for _, n := range e.notes {
		if n.Time >= cut {
			out = append(out, n)
		}
	}
	if len(out) != len(e.notes) {
		e.notes = out
		e.saveNotesLocked()
	}
}

// LatestTag is the text of the newest tag (the default tag of a new note):
// from the live range, else from the ranges of the last 7 days.
func (e *Engine) LatestTag() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n := len(e.live.Tags); n > 0 {
		return e.live.Tags[n-1].Text
	}
	starts := e.store.Starts()
	for i := len(starts) - 1; i >= 0 && i >= len(starts)-7*12; i-- {
		if starts[i].Equal(e.liveStart) {
			continue
		}
		if tags := e.store.Load(starts[i]).Tags; len(tags) > 0 {
			return tags[len(tags)-1].Text
		}
	}
	return ""
}

func newNoteID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
