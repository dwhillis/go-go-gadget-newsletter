package main

import (
	"database/sql"
	"errors"
	"html"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/gorilla/feeds"
	"github.com/julienschmidt/httprouter"
	_ "github.com/mattn/go-sqlite3"
	"github.com/mnako/letters"

	"github.com/emersion/go-smtp"
)

const (
	dbPath          = "./go-go-gadget-newsletter.db"
	entryRetention  = "-3 months"
	cleanupInterval = 24 * time.Hour
	maxFeedBytes    = 10000000
	// Only affects the length of link URLs, so a placeholder of realistic length is close enough for sizing.
	sizingBasePath = "https://example.com"
)

// The Backend implements SMTP server methods.
type Backend struct {
	db *sql.DB
}

func (bkd *Backend) NewSession(_ *smtp.Conn) (smtp.Session, error) {
	return &Session{db: bkd.db}, nil
}

// A Session is returned after EHLO.
type Session struct {
	db   *sql.DB
	from string
	to   []string
}

func (s *Session) AuthPlain(username, password string) error {
	// Credentials are accepted but ignored: nothing is relayed, so there is nothing to protect.
	return nil
}

func (s *Session) Mail(from string, opts *smtp.MailOptions) error {
	s.from = from
	return nil
}

func (s *Session) Rcpt(to string, opts *smtp.RcptOptions) error {
	if _, err := feedNameFromAddress(to); err != nil {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "Invalid recipient"}
	}
	s.to = append(s.to, to)
	return nil
}

func (s *Session) Data(r io.Reader) error {
	email, err := letters.ParseEmail(r)
	if err != nil {
		log.Println("Could not parse email:", err)
		return &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 6, 0}, Message: "Message could not be parsed"}
	}

	log.Println("Received new email")
	log.Println("Date:", email.Headers.Date)
	log.Println("To:", s.to)
	log.Println("Subject:", email.Headers.Subject)

	for _, to := range s.to {
		feedTitle, err := feedNameFromAddress(to)
		if err != nil {
			return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "Invalid recipient"}
		}
		if err := deliver(s.db, feedTitle, &email); err != nil {
			log.Println("Could not deliver to", feedTitle+":", err)
			return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Temporary failure, please retry"}
		}
	}
	return nil
}

func (s *Session) Reset() {
	s.from = ""
	s.to = nil
}

func (s *Session) Logout() error {
	return nil
}

func feedNameFromAddress(address string) (string, error) {
	name, _, found := strings.Cut(address, "@")
	if !found || name == "" {
		return "", errors.New("invalid recipient address: " + address)
	}
	return name, nil
}

// entryContent prefers the HTML body and falls back to the plain text body, escaped and preformatted.
func entryContent(email *letters.Email) string {
	if email.HTML != "" {
		return email.HTML
	}
	return `<pre style="white-space: pre-wrap">` + html.EscapeString(email.Text) + `</pre>`
}

func deliver(db *sql.DB, feedTitle string, email *letters.Email) error {
	author := ""
	if len(email.Headers.From) > 0 {
		author = email.Headers.From[0].Name
		if author == "" {
			author = email.Headers.From[0].Address
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT OR IGNORE INTO feeds(reference, title) VALUES(?, ?)`, feedTitle, feedTitle)
	if err != nil {
		return err
	}
	if created, _ := res.RowsAffected(); created > 0 {
		log.Println(feedTitle + " does not exist. Creating feed.")
	}

	feed, err := getFeedFromTitle(tx, feedTitle)
	if err != nil {
		return err
	}

	// TODO: Consider capping the feed length.
	// Kill-the-newsletter uses 500,000 bytes
	_, err = tx.Exec(`INSERT INTO entries(reference, feed, title, author, content) VALUES(?, ?, ?, ?, ?)`,
		uuid.NewString(),
		feed.id,
		email.Headers.Subject,
		author,
		entryContent(email))
	if err != nil {
		return err
	}

	_, err = tx.Exec(`UPDATE "feeds" SET "updatedAt" = CURRENT_TIMESTAMP WHERE "id" = ?`, feed.id)
	if err != nil {
		return err
	}

	return tx.Commit()
}

type Feed struct {
	id        int
	createdAt time.Time
	updatedAt time.Time
	reference string
	title     string
}

// querier is satisfied by both *sql.DB and *sql.Tx.
type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func getFeedFromTitle(q querier, title string) (Feed, error) {
	row := q.QueryRow(`SELECT "id", "createdAt", "updatedAt", "reference", "title" FROM "feeds" WHERE "reference" = ?`, title)

	feed := Feed{}
	err := row.Scan(&feed.id, &feed.createdAt, &feed.updatedAt, &feed.reference, &feed.title)
	if err != nil {
		return Feed{}, err
	}
	return feed, nil
}

func openDb(path string) (*sql.DB, error) {
	// WAL and a busy timeout let the SMTP handler and the cleanup job write without "database is locked" errors.
	return sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on")
}

func initDb(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS "feeds" (
        "id" INTEGER PRIMARY KEY AUTOINCREMENT,
        "createdAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        "updatedAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        "reference" TEXT NOT NULL UNIQUE,
        "title" TEXT NOT NULL
      );

    CREATE TABLE IF NOT EXISTS "entries" (
        "id" INTEGER PRIMARY KEY AUTOINCREMENT,
        "createdAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        "reference" TEXT NOT NULL UNIQUE,
        "feed" INTEGER NOT NULL REFERENCES "feeds",
        "title" TEXT NOT NULL,
        "author" TEXT NOT NULL,
        "content" TEXT NOT NULL
      );

	CREATE INDEX IF NOT EXISTS "entriesFeed" ON "entries" ("feed");
	`
	_, err := db.Exec(schema)
	return err
}

func makeBasePath(r *http.Request) string {
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	return proto + "://" + r.Host
}

func makeSelfRef(basePath string, path string, value string) string {
	return basePath + "/" + path + "/" + value
}

func renderFeed(db *sql.DB, feed Feed, basePath string) (string, error) {
	outputFeed := feeds.Feed{
		Title:       feed.title,
		Link:        &feeds.Link{Href: makeSelfRef(basePath, "feeds", feed.reference)},
		Description: "",
		Author:      &feeds.Author{Name: feed.title},
		Created:     feed.createdAt}

	entryRows, err := db.Query(`SELECT "createdAt", "reference", "title", "author", "content"
	FROM "entries"
	WHERE "feed" = ?
	ORDER BY "id" DESC`, feed.id)
	if err != nil {
		return "", err
	}
	defer entryRows.Close()

	var outputFeedItems []*feeds.Item
	for entryRows.Next() {
		item := feeds.Item{}
		var reference string
		var author string
		err := entryRows.Scan(&item.Created, &reference, &item.Title, &author, &item.Content)
		if err != nil {
			return "", err
		}
		// Atom IDs must be globally unique IRIs; the row id alone is not.
		item.Id = "urn:uuid:" + reference
		item.Author = &feeds.Author{Name: author}
		item.Link = &feeds.Link{Href: makeSelfRef(basePath, "alternates", reference)}
		item.Description = item.Content
		outputFeedItems = append(outputFeedItems, &item)
	}
	if err := entryRows.Err(); err != nil {
		return "", err
	}
	outputFeed.Items = outputFeedItems
	return (&feeds.Atom{Feed: &outputFeed}).ToAtom()
}

func handleFeed(db *sql.DB) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		feedReference := ps.ByName("feedReference")

		feed, err := getFeedFromTitle(db, feedReference)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Feed not found", http.StatusNotFound)
			return
		}
		if err != nil {
			log.Println(err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		atomFeed, err := renderFeed(db, feed, makeBasePath(r))
		if err != nil {
			log.Println(err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
		w.Header().Set("X-Robots-Tag", "noindex")

		io.WriteString(w, atomFeed)
	}
}

func handleAlternate(db *sql.DB) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		entryReference := ps.ByName("entryReference")

		var content string
		err := db.QueryRow(`SELECT "content" FROM "entries" WHERE "reference" = ?`, entryReference).Scan(&content)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Entry not found", http.StatusNotFound)
			return
		}
		if err != nil {
			log.Println(err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Robots-Tag", "noindex")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Newsletter HTML is untrusted. The sandbox disables scripts, forms, and access to this origin's
		// storage/cookies, but still lets images and styles load. Popups escape the sandbox so links open normally.
		w.Header().Set("Content-Security-Policy", "sandbox allow-popups allow-popups-to-escape-sandbox")

		io.WriteString(w, content)
	}
}

func getFeeds(db *sql.DB) ([]Feed, error) {
	rows, err := db.Query(`SELECT "id", "createdAt", "updatedAt", "reference", "title" FROM "feeds"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	feeds := []Feed{}
	for rows.Next() {
		feed := Feed{}
		err = rows.Scan(&feed.id, &feed.createdAt, &feed.updatedAt, &feed.reference, &feed.title)
		if err != nil {
			return nil, err
		}
		feeds = append(feeds, feed)
	}
	return feeds, rows.Err()
}

func cleanup(db *sql.DB) {
	log.Print("Running cleanup task")
	_, err := db.Exec(`DELETE FROM "entries" WHERE "createdAt" < DATETIME('now', ?)`, entryRetention)
	if err != nil {
		log.Println("Old entry cleanup failed:", err)
		return
	}
	log.Print("3 Month Cleanup successful")

	feeds, err := getFeeds(db)
	if err != nil {
		log.Println("Could not load feeds for cleanup:", err)
		return
	}

	for _, feed := range feeds {
		if err := trimFeed(db, feed); err != nil {
			log.Println("Could not trim", feed.title+":", err)
		}
	}
}

// trimFeed removes the oldest entries until the rendered feed fits under maxFeedBytes.
func trimFeed(db *sql.DB, feed Feed) error {
	for {
		rendered, err := renderFeed(db, feed, sizingBasePath)
		if err != nil {
			return err
		}
		overBy := len(rendered) - maxFeedBytes
		if overBy <= 0 {
			return nil
		}
		log.Printf("%v is %v bytes long, removing about %v bytes\n", feed.title, len(rendered), overBy)

		stored, err := storedBytes(db, feed.id)
		if err != nil {
			return err
		}
		if stored == 0 {
			return nil
		}
		// Rendered output is larger than the stored columns (content is escaped and emitted twice),
		// so convert the overage into stored bytes using the ratio from this render.
		bytesNeeded := max(overBy*stored/len(rendered), 1)

		removed, err := removeOldestEntries(db, feed.id, bytesNeeded)
		if err != nil {
			return err
		}
		if removed == 0 {
			return nil
		}
	}
}

func storedBytes(db *sql.DB, feedID int) (int, error) {
	var total int
	err := db.QueryRow(`SELECT COALESCE(SUM(LENGTH(CAST("title" AS BLOB)) + LENGTH(CAST("author" AS BLOB)) + LENGTH(CAST("content" AS BLOB))), 0)
	FROM "entries"
	WHERE "feed" = ?`, feedID).Scan(&total)
	return total, err
}

// removeOldestEntries deletes oldest-first entries until about bytesNeeded has been freed.
func removeOldestEntries(db *sql.DB, feedID int, bytesNeeded int) (int, error) {
	rows, err := db.Query(`SELECT "id", LENGTH(CAST("title" AS BLOB)) + LENGTH(CAST("author" AS BLOB)) + LENGTH(CAST("content" AS BLOB))
	FROM "entries"
	WHERE "feed" = ?
	ORDER BY "id" ASC`, feedID)
	if err != nil {
		return 0, err
	}

	var ids []int
	freed := 0
	for freed < bytesNeeded && rows.Next() {
		var id, size int
		if err := rows.Scan(&id, &size); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
		freed += size
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM "entries" WHERE "id" = ?`, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func main() {
	db, err := openDb(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := initDb(db); err != nil {
		log.Fatal(err)
	}

	// Cleanup cron task
	scheduler, err := gocron.NewScheduler()
	if err != nil {
		log.Fatal(err)
	}
	j, err := scheduler.NewJob(
		gocron.DurationJob(
			cleanupInterval,
		),
		gocron.NewTask(cleanup, db),
	)
	if err != nil {
		log.Fatal(err)
	}

	// start the scheduler
	scheduler.Start()

	err = j.RunNow()
	if err != nil {
		log.Fatal(err)
	}

	// SMTP Server
	be := &Backend{db: db}

	s := smtp.NewServer(be)

	s.Addr = ":25"
	s.Domain = "localhost"
	s.ReadTimeout = 10 * time.Second
	s.WriteTimeout = 10 * time.Second
	s.MaxMessageBytes = 1024 * 1024
	s.MaxRecipients = 50
	s.AllowInsecureAuth = true

	log.Println("Starting mail server at", s.Addr)
	go func() {
		if err := s.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()

	// HTTP Server
	router := httprouter.New()
	router.GET("/feeds/:feedReference", handleFeed(db))
	router.GET("/alternates/:entryReference", handleAlternate(db))

	server := &http.Server{
		Addr:              ":8080",
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Println("Starting http server at", server.Addr)
	err = server.ListenAndServe()
	if err != nil {
		log.Fatal(err)
	}
}
