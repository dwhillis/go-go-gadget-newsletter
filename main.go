package main

import (
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/feeds"
	"github.com/julienschmidt/httprouter"
	_ "github.com/mattn/go-sqlite3"

	"github.com/emersion/go-smtp"
)

// The Backend implements SMTP server methods.
type Backend struct{}

func (bkd *Backend) NewSession(_ *smtp.Conn) (smtp.Session, error) {
	return &Session{}, nil
}

// A Session is returned after EHLO.
type Session struct {
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
	m, err := mail.ReadMessage(r)
	if err != nil {
		log.Fatal(err)
	}

	header := m.Header
	log.Println("Received new email")
	log.Println("Date:", header.Get("Date"))
	log.Println("To:", header.Get("To"))
	log.Println("From:", header.Get("From"))
	log.Println("Subject:", header.Get("Subject"))

	body, err := io.ReadAll(m.Body)
	if err != nil {
		log.Fatal(err)
	}

	db := openDb()
	defer db.Close()
	feedTitle := strings.Split(header.Get("To"), "@")[0]
	feed, err := getFeedFromTitle(db, feedTitle)
	if err == ErrIDNotFound {
		log.Println(feedTitle + " does not exist. Creating feed.")
		_, err = db.Exec(`INSERT INTO feeds(reference, title) VALUES(?, ?)`, feedTitle, feedTitle)
		if err != nil {
			log.Fatal(err)
		}
		feed, err = getFeedFromTitle(db, feedTitle)
		if err != nil {
			log.Fatal(err)
		}
	}

	title := header.Get("Subject")
	address, err := mail.ParseAddress(header.Get("From"))
	if err != nil {
		log.Fatal(err)
	}
	author := address.Name
	_, err = db.Exec(`INSERT INTO entries(reference, feed, title, author, content) 
	VALUES(?, ?, ?, ?, ?)`,
		uuid.NewString(),
		feed.id,
		title,
		author,
		body)

	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`UPDATE "feeds" SET "updatedAt" = CURRENT_TIMESTAMP WHERE "id" = ?`, feed.id)

	if err != nil {
		log.Fatal(err)
	}

	// TODO: Consider capping the feed length.
	// Kill-the-newsletter uses 500,000
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
	db, err := sql.Open("sqlite3", "./foo.db")
	if err != nil {
		log.Panic(err)
	}
	return db
}
func initDb() {
	db := openDb()
	defer db.Close()

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

func makeSelfRef(r *http.Request, path string, value string) string {
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	return proto + "://" + r.Host + "/" + path + "/" + value
}

func handleFeed(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	feedReference := ps.ByName("feedReference")
	db := openDb()
	defer db.Close()

	feed, err := getFeedFromTitle(db, feedReference)
	if err != nil {
		io.WriteString(w, "Feed not found")
		return
	}

	outputFeed := feeds.Feed{
		Title:       feed.title,
		Link:        &feeds.Link{Href: makeSelfRef(r, "feeds", feed.reference)},
		Description: "",
		Author:      &feeds.Author{Name: feed.title},
		Created:     feed.createdAt}

	entryRows, err := db.Query(`SELECT "id", "createdAt", "reference", "title", "author", "content"
	FROM "entries"
	WHERE "feed" = ?
	ORDER BY "id" DESC`, feed.id)
	if err != nil {
		log.Fatal(err)
	}
	defer entryRows.Close()

	var outputFeedItems []*feeds.Item
	for entryRows.Next() {
		item := feeds.Item{}
		var reference string
		var author string
		err := entryRows.Scan(&item.Id, &item.Created, &reference, &item.Title, &author, &item.Content)
		if err != nil {
			log.Fatal(err)
		}
		item.Author = &feeds.Author{Name: author}
		item.Link = &feeds.Link{Href: makeSelfRef(r, "alternates", reference)}
		outputFeedItems = append(outputFeedItems, &item)
	}
	outputFeed.Items = outputFeedItems
	rssFeed, err := (&feeds.Rss{Feed: &outputFeed}).ToRss()
	if err != nil {
		log.Fatal(err)
	}

	w.Header().Set("Content-Type", "application/atom+xml")
	w.Header().Set("X-Robots-Tag", "noindex")

	io.WriteString(w, rssFeed)
}

func handleAlternate(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	entryReference := ps.ByName("entryReference")
	db := openDb()
	defer db.Close()

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

func main() {
	initDb()
	be := &Backend{}

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

	router := httprouter.New()
	router.GET("/feeds/:feedReference", handleFeed)
	router.GET("/alternates/:entryReference", handleAlternate)

	log.Println("Starting http server at", ":80")
	err := http.ListenAndServe(":80", router)
	if err != nil {
		log.Fatal(err)
	}
}
