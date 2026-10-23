package main

import (
	"testing"
	"time"
)

func TestNotesCRUD(t *testing.T) {
	dir := t.TempDir()
	e, err := newEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := e.SaveNote(Note{Title: "  ", Time: now.UnixMilli()}); err == nil {
		t.Fatal("empty title must fail")
	}
	if _, err := e.SaveNote(Note{Title: "old", Time: now.Add(-(keepDays + 1) * 24 * time.Hour).UnixMilli()}); err == nil {
		t.Fatal("date older than keepDays must fail")
	}
	e.SaveNote(Note{Title: "older", Time: now.Add(-time.Hour).UnixMilli(), Tag: "4G"})
	l, err := e.SaveNote(Note{Title: "newer", Time: now.UnixMilli(), Body: "text\n"})
	if err != nil || len(l) != 2 || l[0].Title != "newer" || l[0].Body != "text" {
		t.Fatalf("got %+v, %v", l, err)
	}
	// Update keeps the ID and Created, and the list is re-sorted by Time.
	up := l[1]
	up.Title, up.Time = "moved", now.Add(time.Hour).UnixMilli()
	l, err = e.SaveNote(up)
	if err != nil || len(l) != 2 || l[0].ID != up.ID || l[0].Title != "moved" || l[0].Created != up.Created {
		t.Fatalf("update: %+v, %v", l, err)
	}
	if _, err := e.SaveNote(Note{ID: "nope", Title: "x", Time: now.UnixMilli()}); err == nil {
		t.Fatal("unknown ID must fail")
	}
	// Saved to disk.
	if got := loadNotes(dir); len(got) != 2 {
		t.Fatalf("on disk: %d notes", len(got))
	}
	if l = e.DeleteNote(up.ID); len(l) != 1 || l[0].Title != "newer" {
		t.Fatalf("delete: %+v", l)
	}
}

func TestNotesPurge(t *testing.T) {
	e, _ := newEngine(t.TempDir())
	now := time.Now()
	e.notes = []Note{
		{ID: "a", Title: "keep", Time: now.UnixMilli()},
		{ID: "b", Title: "drop", Time: now.Add(-(keepDays + 1) * 24 * time.Hour).UnixMilli()},
	}
	e.purgeNotesLocked()
	if len(e.notes) != 1 || e.notes[0].ID != "a" {
		t.Fatalf("got %+v", e.notes)
	}
}

func TestLatestTag(t *testing.T) {
	e, _ := newEngine(t.TempDir())
	if got := e.LatestTag(); got != "" {
		t.Fatalf("got %q", got)
	}
	e.AddTag("WiFi")
	e.AddTag("4G")
	if got := e.LatestTag(); got != "4G" {
		t.Fatalf("got %q", got)
	}
}
