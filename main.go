package main

import (
	"database/sql"
	"errors"
	"fmt"
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

// The Backend implements SMTP server methods.
type Backend struct{
	db *sql.DB
}

func (bkd *Backend) NewSession(_ *smtp.Conn) (smtp.Session, error) {
	return &Session{db: bkd.db}, nil
}

// A Session is returned after EHLO.
type Session struct {
	db   *sql.DB
	auth bool
	from string
	to   string
}

func (s *Session) AuthPlain(username, password string) error {
	// Don't care about auth
	s.auth = true
	return nil
}

func (s *Session) Mail(from string, opts *smtp.MailOptions) error {
	s.from = from
	return nil
}

func (s *Session) Rcpt(to string, opts *smtp.RcptOptions) error {
	s.to = to
	return nil
}

func (s *Session) Data(r io.Reader) error {
	email, err := letters.ParseEmail(r)
	if err != nil {
		log.Printf("Error parsing email: %v", err)
		return err
	}

	toAddress := s.to
	if len(email.Headers.To) > 0 && email.Headers.To[0].Address != "" {
		toAddress = email.Headers.To[0].Address
	}

	fromAddress := "unknown"
	author := "unknown"
	if len(email.Headers.From) > 0 {
		fromAddress = email.Headers.From[0].Address
		if email.Headers.From[0].Name != "" {
			author = email.Headers.From[0].Name
		} else {
			author = fromAddress
		}
	}

	log.Println("Received new email")
	log.Println("Date:", email.Headers.Date)
	log.Println("To:", toAddress)
	log.Println("From:", fromAddress)
	log.Println("Subject:", email.Headers.Subject)

	feedTitle := strings.Split(s.to, "@")[0]
	feed, err := getFeedFromTitle(s.db, feedTitle)
	if err == ErrIDNotFound {
		log.Println(feedTitle + " does not exist. Creating feed.")
		_, err = s.db.Exec(`INSERT INTO feeds(reference, title) VALUES(?, ?)`, feedTitle, feedTitle)
		if err != nil {
			log.Printf("Error inserting new feed: %v", err)
			return err
		}
		feed, err = getFeedFromTitle(s.db, feedTitle)
		if err != nil {
			log.Printf("Error retrieving newly created feed: %v", err)
			return err
		}
	}

	title := email.Headers.Subject
	_, err = s.db.Exec(`INSERT INTO entries(reference, feed, title, author, content)
	VALUES(?, ?, ?, ?, ?)`,
		uuid.NewString(),
		feed.id,
		title,
		author,
		email.HTML)

	if err != nil {
		log.Printf("Error inserting new entry: %v", err)
		return err
	}

	_, err = s.db.Exec(`UPDATE "feeds" SET "updatedAt" = CURRENT_TIMESTAMP WHERE "id" = ?`, feed.id)

	if err != nil {
		log.Printf("Error updating feed timestamp: %v", err)
		return err
	}

	// TODO: Consider capping the feed length.
	// Kill-the-newsletter uses 500,000 bytes
	return nil
}

func (s *Session) Reset() {}

func (s *Session) Logout() error {
	return nil
}

type Feed struct {
	id        int
	createdAt time.Time
	updatedAt time.Time
	reference string
	title     string
}

var ErrIDNotFound = errors.New("id not found")

func getFeedFromTitle(db *sql.DB, title string) (Feed, error) {
	row := db.QueryRow("SELECT * FROM feeds WHERE reference = ?", title)

	feed := Feed{}
	err := row.Scan(&feed.id, &feed.createdAt, &feed.updatedAt, &feed.reference, &feed.title)
	if err != nil {
		log.Println(err)
		return Feed{}, ErrIDNotFound
	}
	return feed, nil
}

func openDb() *sql.DB {
	db, err := sql.Open("sqlite3", "./go-go-gadget-newsletter.db")
	if err != nil {
		log.Panic(err)
	}
	return db
}
func initDb(db *sql.DB) {
	sql := `
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
	_, err := db.Exec(sql)
	if err != nil {
		db.Close()
		log.Fatal(err)
	}
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

	entryRows, err := db.Query(`SELECT "id", "createdAt", "reference", "title", "author", "content"
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
		err := entryRows.Scan(&item.Id, &item.Created, &reference, &item.Title, &author, &item.Content)
		if err != nil {
			return "", err
		}
		item.Author = &feeds.Author{Name: author}
		item.Link = &feeds.Link{Href: makeSelfRef(basePath, "alternates", reference)}
		item.Description = item.Content
		outputFeedItems = append(outputFeedItems, &item)
	}
	outputFeed.Items = outputFeedItems
	atomFeed, err := (&feeds.Atom{Feed: &outputFeed}).ToAtom()
	if err != nil {
		return "", err
	}

	return atomFeed, nil
}

func handleFeed(db *sql.DB) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		feedReference := ps.ByName("feedReference")

		feed, err := getFeedFromTitle(db, feedReference)
		if err != nil {
			io.WriteString(w, "Feed not found")
			return
		}

		atomFeed, err := renderFeed(db, feed, makeBasePath(r))
		if err != nil {
			http.Error(w, "Error rendering feed", http.StatusInternalServerError)
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

		row := db.QueryRow(`SELECT "content" FROM entries WHERE reference = ?`, entryReference)
		var content string
		err := row.Scan(&content)
		if err != nil {
			io.WriteString(w, "Entry not found")
			return
		}

		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Robots-Tag", "noindex")

		io.WriteString(w, content)
	}
}

func getFeeds(db *sql.DB) []Feed {
	entryRows, err := db.Query("SELECT * FROM feeds")
	if err != nil {
		log.Printf("Error querying feeds: %v", err)
		return []Feed{}
	}
	defer entryRows.Close()

	feeds := []Feed{}
	for entryRows.Next() {
		feed := Feed{}
		err = entryRows.Scan(&feed.id, &feed.createdAt, &feed.updatedAt, &feed.reference, &feed.title)
		if err != nil {
			log.Printf("Error scanning feed: %v", err)
			continue
		}
		feeds = append(feeds, feed)
	}
	return feeds
}

func cleanup(db *sql.DB) {
	log.Print("Running cleanup task")
	_, err := db.Exec(`DELETE FROM entries WHERE createdAt < DATE('now', '-3 months')`)
	if err != nil {
		log.Printf("Error running 3 month cleanup: %v", err)
		return
	}
	log.Print("3 Month Cleanup successful")

	feeds := getFeeds(db)

	for _, feed := range feeds {
		keepChecking := true
		for keepChecking {
			renderedFeed, err := renderFeed(db, feed, "http://google.com")
			if err != nil {
				log.Printf("Error rendering feed for cleanup check: %v", err)
				break
			}
			feedLength := len(renderedFeed)
			log.Printf("%v is %v bytes long\n", feed.title, feedLength)
			if feedLength > 10000000 {
				log.Println("That's too long. Deleting the last entry")
				_, err := db.Exec(`DELETE FROM entries WHERE id = (SELECT id FROM entries WHERE feed = ? ORDER BY createdAt ASC LIMIT 1)`, feed.id)
				if err != nil {
					log.Printf("Error deleting old entry for feed cleanup: %v", err)
					break
				}
				keepChecking = true
			} else {
				keepChecking = false
			}
		}

	}
}

func main() {
	db := openDb()
	defer db.Close()
	initDb(db)

	// Cleanup cron task
	scheduler, err := gocron.NewScheduler()
	if err != nil {
		log.Fatal(err)
	}
	j, err := scheduler.NewJob(
		gocron.DurationJob(
			24*time.Hour,
		),
		gocron.NewTask(func() { cleanup(db) }),
	)
	if err != nil {
		log.Fatal(err)
	}
	// each job has a unique id
	fmt.Println(j.ID())

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
	go s.ListenAndServe()

	// HTTP Server
	router := httprouter.New()
	router.GET("/feeds/:feedReference", handleFeed(db))
	router.GET("/alternates/:entryReference", handleAlternate(db))

	log.Println("Starting http server at", ":8080")
	err = http.ListenAndServe(":8080", router)
	if err != nil {
		log.Fatal(err)
	}
}
