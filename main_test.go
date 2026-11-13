package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/julienschmidt/httprouter"
)

func newTestDb(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDb(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := initDb(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func deliverRaw(t *testing.T, db *sql.DB, rcpt string, raw string) error {
	t.Helper()
	s := &Session{db: db}
	if err := s.Rcpt(rcpt, nil); err != nil {
		t.Fatal(err)
	}
	return s.Data(strings.NewReader(raw))
}

const htmlEmail = "From: Jane Doe <jane@example.com>\r\n" +
	"To: news@rss.example.com\r\n" +
	"Subject: Issue 1\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>Hello <b>world</b></p>"

const plainEmail = "From: jane@example.com\r\n" +
	"To: news@rss.example.com\r\n" +
	"Subject: Plain\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"1 < 2 & 3"

func TestDeliverCreatesFeedAndEntry(t *testing.T) {
	db := newTestDb(t)

	if err := deliverRaw(t, db, "news@rss.example.com", htmlEmail); err != nil {
		t.Fatal(err)
	}

	feed, err := getFeedFromTitle(db, "news")
	if err != nil {
		t.Fatal(err)
	}
	atom, err := renderFeed(db, feed, "https://rss.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Issue 1", "Jane Doe", "urn:uuid:"} {
		if !strings.Contains(atom, want) {
			t.Errorf("feed missing %q", want)
		}
	}
}

func TestPlainTextFallbackIsEscaped(t *testing.T) {
	db := newTestDb(t)

	if err := deliverRaw(t, db, "news@rss.example.com", plainEmail); err != nil {
		t.Fatal(err)
	}

	var content string
	if err := db.QueryRow(`SELECT "content" FROM "entries"`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "1 &lt; 2 &amp; 3") {
		t.Errorf("plain text not escaped, got %q", content)
	}
}

func TestMissingHeadersDoNotPanic(t *testing.T) {
	db := newTestDb(t)

	raw := "Subject: no sender\r\n\r\nbody"
	if err := deliverRaw(t, db, "news@rss.example.com", raw); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidRecipientRejected(t *testing.T) {
	s := &Session{}
	if err := s.Rcpt("@rss.example.com", nil); err == nil {
		t.Error("expected empty local part to be rejected")
	}
}

func TestAlternateHasSandboxHeaders(t *testing.T) {
	db := newTestDb(t)
	if err := deliverRaw(t, db, "news@rss.example.com", htmlEmail); err != nil {
		t.Fatal(err)
	}

	var reference string
	if err := db.QueryRow(`SELECT "reference" FROM "entries"`).Scan(&reference); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/alternates/"+reference, nil)
	handleAlternate(db)(rec, req, httprouter.Params{{Key: "entryReference", Value: reference}})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox") {
		t.Errorf("missing sandbox CSP, got %q", csp)
	}
	if !strings.Contains(rec.Body.String(), "<b>world</b>") {
		t.Errorf("body was altered: %q", rec.Body.String())
	}
}

func TestMissingFeedReturns404(t *testing.T) {
	db := newTestDb(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/feeds/nope", nil)
	handleFeed(db)(rec, req, httprouter.Params{{Key: "feedReference", Value: "nope"}})

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestCleanupTrimsFeedToLimit(t *testing.T) {
	db := newTestDb(t)

	if _, err := db.Exec(`INSERT INTO "feeds"("reference", "title") VALUES('big', 'big')`); err != nil {
		t.Fatal(err)
	}
	// Each entry is about 1 MB, so 15 of them overflow the 10 MB limit.
	body := strings.Repeat("x", 1000000)
	for i := 0; i < 15; i++ {
		if _, err := db.Exec(`INSERT INTO "entries"("reference", "feed", "title", "author", "content")
			VALUES(?, 1, 't', 'a', ?)`, "ref-"+strings.Repeat("0", i), body); err != nil {
			t.Fatal(err)
		}
	}

	cleanup(db)

	feed, err := getFeedFromTitle(db, "big")
	if err != nil {
		t.Fatal(err)
	}
	atom, err := renderFeed(db, feed, sizingBasePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(atom) > maxFeedBytes {
		t.Errorf("feed is %d bytes, want <= %d", len(atom), maxFeedBytes)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM "entries"`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining == 0 || remaining == 15 {
		t.Errorf("remaining entries = %d, expected some but not all to be removed", remaining)
	}
}
