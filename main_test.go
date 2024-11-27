package main

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestGetFeedFromTitleEmptyReference(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	initDb(db)

	_, err = getFeedFromTitle(db, "")
	if err == nil {
		t.Error("Expected error for empty reference, got nil")
	}
}

func TestGetFeedFromTitleNonExistent(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	initDb(db)

	_, err = getFeedFromTitle(db, "nonexistent_feed")
	if err == nil {
		t.Error("Expected error for non-existent feed, got nil")
	}
}

func TestGetFeedFromTitleMultipleFeeds(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	initDb(db)

	feeds := []struct {
		reference string
		title     string
	}{
		{"feed1", "First Feed"},
		{"feed2", "Second Feed"},
		{"feed3", "Third Feed"},
	}

	for _, f := range feeds {
		_, err = db.Exec(`INSERT INTO feeds (reference, title) VALUES (?, ?)`, f.reference, f.title)
		if err != nil {
			t.Fatalf("Failed to insert feed: %v", err)
		}
	}

	for _, f := range feeds {
		feed, err := getFeedFromTitle(db, f.reference)
		if err != nil {
			t.Errorf("Expected no error for reference %s, got %v", f.reference, err)
		}
		if feed.title != f.title {
			t.Errorf("Expected title %v for reference %s, got %v", f.title, f.reference, feed.title)
		}
	}
}

func TestGetFeedsEmptyDatabase(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	initDb(db)

	feeds := getFeeds(db)
	if len(feeds) != 0 {
		t.Errorf("Expected empty feeds slice, got %d feeds", len(feeds))
	}
}

func TestGetFeedsMultipleEntries(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	initDb(db)

	expectedFeeds := []struct {
		reference string
		title     string
	}{
		{"ref1", "Feed One"},
		{"ref2", "Feed Two"},
		{"ref3", "Feed Three"},
		{"ref4", "Feed Four"},
	}

	for _, f := range expectedFeeds {
		_, err = db.Exec(`INSERT INTO feeds (reference, title) VALUES (?, ?)`, f.reference, f.title)
		if err != nil {
			t.Fatalf("Failed to insert test feed: %v", err)
		}
	}

	feeds := getFeeds(db)
	if len(feeds) != len(expectedFeeds) {
		t.Errorf("Expected %d feeds, got %d", len(expectedFeeds), len(feeds))
	}

	for i, f := range feeds {
		if f.reference != expectedFeeds[i].reference {
			t.Errorf("Expected reference %s, got %s", expectedFeeds[i].reference, f.reference)
		}
		if f.title != expectedFeeds[i].title {
			t.Errorf("Expected title %s, got %s", expectedFeeds[i].title, f.title)
		}
	}
}
