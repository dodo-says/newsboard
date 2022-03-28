package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	sqlite "github.com/mattn/go-sqlite3"
	"github.com/shurcooL/github_flavored_markdown"
	"golang.org/x/crypto/bcrypt"
)

const ADMIN_ID = 1

const SUBMISSION = 0
const COMMENT = 1

const SETTINGS_LIMIT = 30

type User struct {
	Userid   int64
	Username string
	Active   bool
	Email    string
}

type Site struct {
	Title    string
	Desc     string
	Gravityf float64
}

type Entry struct {
	Entryid  int64
	Thing    int
	Title    string
	Url      string
	Body     string
	Createdt string
	Userid   int64
	Parentid int64
}

type Cat struct {
	Catid int64
	Name  string
}

type VoteResult struct {
	Entryid    int64 `json:"entryid"`
	Userid     int64 `json:"userid"`
	TotalVotes int   `json:"totalvotes"`
}

type QIndex struct {
	Latest   string
	Username string
	Cat      int64
	Tag      string
}

var (
	developMode = flag.Bool("dev", false, "Develop mode, auto reload templates")
	db          = flag.String("db", "news.db", "db file")
	addr        = flag.String("addr", ":8000", "Port to listen on")
	initData    = flag.Bool("init", false, "Initialize database")
)

func main() {
	flag.Parse()

	if *initData {
		if fileExists(*db) {
			fmt.Printf("File '%s' already exists. Can't initialize it.\n", *db)
			os.Exit(1)
		}
		createAndInitTables(*db)
		os.Exit(0)
	}

	// Exit if specified notes file doesn't exist.
	if !fileExists(*db) {
		fmt.Printf(`Newboard file '%s' doesn't exist. Create one using:
		nb -i <newsboard_file>
	`, *db)
		os.Exit(1)
	}

	registerSqliteFuncs()
	db, err := sql.Open("sqlite3_custom", *db)
	if err != nil {
		fmt.Printf("Error opening '%s' (%s)\n", *db, err)
		os.Exit(1)
	}

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))
	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "./static/news-paper.ico") })
	http.HandleFunc("/login/", loginHandler(db))
	http.HandleFunc("/logout/", logoutHandler(db))
	http.HandleFunc("/createaccount/", createaccountHandler(db))
	http.HandleFunc("/adminsetup/", adminsetupHandler(db))
	http.HandleFunc("/usersetup/", usersetupHandler(db))
	http.HandleFunc("/edituser/", edituserHandler(db))
	http.HandleFunc("/activateuser/", activateuserHandler(db))
	http.HandleFunc("/createcat/", createcatHandler(db))
	http.HandleFunc("/editcat/", editcatHandler(db))
	http.HandleFunc("/delcat/", delcatHandler(db))
	http.HandleFunc("/", indexHandler(db))
	http.HandleFunc("/item/", itemHandler(db))
	http.HandleFunc("/submit/", submitHandler(db))
	http.HandleFunc("/edit/", editHandler(db))
	http.HandleFunc("/del/", delHandler(db))
	http.HandleFunc("/vote/", voteHandler(db))
	http.HandleFunc("/unvote/", unvoteHandler(db))

	fmt.Printf("Listening on %s...\n", *addr)
	err = http.ListenAndServe(*addr, nil)
	log.Fatal(err)
}

func registerSqliteFuncs() {
	rand.Seed(time.Now().UnixNano())

	sql.Register("sqlite3_custom", &sqlite.SQLiteDriver{
		ConnectHook: func(con *sqlite.SQLiteConn) error {
			err := con.RegisterFunc("pow", pow, true)
			if err != nil {
				return err
			}
			err = con.RegisterFunc("randint", randint, false)
			if err != nil {
				return err
			}
			err = con.RegisterFunc("seconds_since_epoch", seconds_since_epoch, false)
			if err != nil {
				return err
			}
			err = con.RegisterFunc("seconds_since_time", seconds_since_time, false)
			if err != nil {
				return err
			}
			err = con.RegisterFunc("hours_since_time", hours_since_time, false)
			if err != nil {
				return err
			}
			err = con.RegisterFunc("calculate_points", calculate_points, false)
			if err != nil {
				return err
			}
			return nil
		},
	})
}
func pow(n int, p float64) float64 {
	return math.Pow(float64(n), p)
}
func randint(n int) int {
	return rand.Intn(n)
}
func seconds_since_epoch(dt string) int64 {
	t, _ := time.Parse(time.RFC3339, dt)
	return t.Unix()
}
func seconds_since_time(dt string) int64 {
	return time.Now().Unix() - seconds_since_epoch(dt)
}
func hours_since_time(dt string) int64 {
	return seconds_since_time(dt) / 60 / 60
}
func calculate_points(votes int, submitdt string, gravityf float64) float64 {
	return float64(votes) / pow((int(hours_since_time(submitdt))+2), gravityf)
}

func fileExists(file string) bool {
	_, err := os.Stat(file)
	if err != nil && os.IsNotExist(err) {
		return false
	}
	return true
}

func idtoi(sid string) int64 {
	if sid == "" {
		return -1
	}
	n, err := strconv.Atoi(sid)
	if err != nil {
		return -1
	}
	return int64(n)
}

func atoi(s string) int {
	if s == "" {
		return -1
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

func atof(s string) float64 {
	if s == "" {
		return -1.0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1.0
	}
	return f
}

func sqlstmt(db *sql.DB, s string) *sql.Stmt {
	stmt, err := db.Prepare(s)
	if err != nil {
		log.Fatalf("db.Prepare() sql: '%s'\nerror: '%s'", s, err)
	}
	return stmt
}

func sqlexec(db *sql.DB, s string, pp ...interface{}) (sql.Result, error) {
	stmt := sqlstmt(db, s)
	defer stmt.Close()
	return stmt.Exec(pp...)
}

func txstmt(tx *sql.Tx, s string) *sql.Stmt {
	stmt, err := tx.Prepare(s)
	if err != nil {
		log.Fatalf("tx.Prepare() sql: '%s'\nerror: '%s'", s, err)
	}
	return stmt
}

func txexec(tx *sql.Tx, s string, pp ...interface{}) (sql.Result, error) {
	stmt := txstmt(tx, s)
	defer stmt.Close()
	return stmt.Exec(pp...)
}

func parseMarkdown(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	return string(github_flavored_markdown.Markdown([]byte(s)))
}

func parseTextLinks(body string) string {
	sre := `\b(https?://\S+)`
	re := regexp.MustCompile(sre)
	body = re.ReplaceAllString(body, "<$1>")
	return body
}

func parseIsoDate(dt string) string {
	tdt, _ := time.Parse(time.RFC3339, dt)
	return tdt.Format("2 Jan 2006")
}

func createAndInitTables(newfile string) {
	if fileExists(newfile) {
		s := fmt.Sprintf("File '%s' already exists. Can't initialize it.\n", newfile)
		fmt.Printf(s)
		os.Exit(1)
	}

	db, err := sql.Open("sqlite3", newfile)
	if err != nil {
		fmt.Printf("Error opening '%s' (%s)\n", newfile, err)
		os.Exit(1)
	}

	ss := []string{
		"BEGIN TRANSACTION;",
		`CREATE TABLE entry (entry_id INTEGER PRIMARY KEY NOT NULL, thing INTEGER NOT NULL DEFAULT 0, title TEXT NOT NULL DEFAULT '', url TEXT NOT NULL DEFAULT '', body TEXT NOT NULL DEFAULT '', createdt TEXT NOT NULL, user_id INTEGER NOT NULL, parent_id INTEGER DEFAULT 0);`,
		`CREATE TABLE user (user_id INTEGER PRIMARY KEY NOT NULL, username TEXT, password TEXT, active INTEGER NOT NULL, email TEXT, CONSTRAINT unique_username UNIQUE (username));`,
		`INSERT INTO user (user_id, username, password, active, email) VALUES (1, 'admin', '', 1, 'admin@localhost');`,
		`CREATE TABLE entryvote(entry_id INTEGER NOT NULL, user_id INTEGER, PRIMARY KEY (entry_id, user_id));`,
		`CREATE TABLE entrytag(entry_id INTEGER NOT NULL, tag TEXT NOT NULL)`,
		`CREATE TABLE cat(cat_id INTEGER PRIMARY KEY NOT NULL, name TEXT NOT NULL)`,
		`CREATE TABLE entrycat(entry_id INTEGER NOT NULL, cat_id INTEGER NOT NULL)`,
		`INSERT INTO cat (cat_id, name) VALUES (1, 'Main');`,
		`CREATE TABLE site (site_id INTEGER PRIMARY KEY NOT NULL, title TEXT NOT NULL, desc TEXT NOT NULL, gravityf REAL NOT NULL);`,
		`INSERT INTO site (site_id, title, desc, gravityf) VALUES (1, 'newsboard', '', 1.0);`,
		`CREATE VIEW totalvotes 
AS 
SELECT entry_id, COUNT(*) AS votes FROM entryvote GROUP BY entry_id;`,
		`INSERT INTO entry (entry_id, thing, title, url, body, createdt, user_id, parent_id) VALUES (1, 0, 'newsboard - a hackernews clone', 'https://github.com/robdelacruz/newsboard', '', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), 1, 0);`,
		`INSERT INTO entrycat (entry_id, cat_id) VALUES (1, 1);`,
		"COMMIT;",
	}

	for _, s := range ss {
		_, err := sqlexec(db, s)
		if err != nil {
			log.Printf("DB error setting up newsboard db on '%s' (%s)\n", newfile, err)
			os.Exit(1)
		}
	}
}

func getLoginUser(r *http.Request, db *sql.DB) *User {
	var u User
	u.Userid = -1

	c, err := r.Cookie("userid")
	if err != nil {
		return &u
	}
	userid := idtoi(c.Value)
	if userid == -1 {
		return &u
	}
	return queryUser(db, userid)
}

func queryUser(db *sql.DB, userid int64) *User {
	var u User
	u.Userid = -1

	s := "SELECT user_id, username, active, email FROM user WHERE user_id = ?"
	row := db.QueryRow(s, userid)
	err := row.Scan(&u.Userid, &u.Username, &u.Active, &u.Email)
	if err == sql.ErrNoRows {
		return &u
	}
	if err != nil {
		fmt.Printf("queryUser() db error (%s)\n", err)
		return &u
	}
	return &u
}

func queryUsername(db *sql.DB, username string) *User {
	var u User
	u.Userid = -1

	s := "SELECT user_id, username, active, email FROM user WHERE username = ?"
	row := db.QueryRow(s, username)
	err := row.Scan(&u.Userid, &u.Username, &u.Active, &u.Email)
	if err == sql.ErrNoRows {
		return &u
	}
	if err != nil {
		fmt.Printf("queryUser() db error (%s)\n", err)
		return &u
	}
	return &u
}

func querySite(db *sql.DB) *Site {
	var site Site
	s := "SELECT title, desc, gravityf FROM site WHERE site_id = 1"
	row := db.QueryRow(s)
	err := row.Scan(&site.Title, &site.Desc, &site.Gravityf)
	if err == sql.ErrNoRows {
		// Site settings row not defined yet, just use default Site values.
		site.Title = "newsboard"
		site.Desc = ""
		site.Gravityf = 1.5
	} else if err != nil {
		// DB error, log then use common site settings.
		log.Printf("error reading site settings for siteid %d (%s)\n", 1, err)
		site.Title = "newsboard"
		site.Gravityf = 1.5
	}
	if site.Title == "" {
		site.Title = "newsboard"
	}
	return &site
}

func queryCat(db *sql.DB, catid int64) *Cat {
	var cat Cat
	s := "SELECT cat_id, name FROM cat WHERE cat_id = ?"
	row := db.QueryRow(s, catid)
	err := row.Scan(&cat.Catid, &cat.Name)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		fmt.Printf("queryCat() db error (%s)\n", err)
		return nil
	}
	return &cat
}

func printPageHead(w io.Writer, site *Site, handleVote bool) {
	getTemplate().ExecuteTemplate(w, "header", map[string]any{
		"Title":      site.Title,
		"HandleVote": handleVote,
	})
}

func printPageFoot(w io.Writer) {
	getTemplate().ExecuteTemplate(w, "footer", nil)
}

func printPageNav(w http.ResponseWriter, login *User, site *Site, qq *QIndex) {
	getTemplate().ExecuteTemplate(w, "pageNav", map[string]any{
		"Login":  login,
		"Site":   site,
		"QIndex": qq,
	})
}

func isCorrectPassword(inputPassword, hashedpwd string) bool {
	if hashedpwd == "" && inputPassword == "" {
		return true
	}
	err := bcrypt.CompareHashAndPassword([]byte(hashedpwd), []byte(inputPassword))
	if err != nil {
		return false
	}
	return true
}

func hashPassword(pwd string) string {
	hashedpwd, err := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return string(hashedpwd)
}

func loginUser(w http.ResponseWriter, userid int64) {
	suserid := fmt.Sprintf("%d", userid)
	c := http.Cookie{
		Name:     "userid",
		Value:    suserid,
		Path:     "/",
		HttpOnly: true,
		// Expires: time.Now().Add(24 * time.Hour),
	}
	http.SetCookie(w, &c)
}

func unescapeUrl(qurl string) string {
	returl := "/"
	if qurl != "" {
		returl, _ = url.QueryUnescape(qurl)
	}
	return returl
}

func escape(s string) string {
	return html.EscapeString(s)
}

func normalizeTitle(title string) string {
	if len(title) > 256 {
		return title[:256]
	}
	return title
}

func loginHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var errmsg string
		var f struct{ username, password string }

		login := getLoginUser(r, db)
		qfrom := r.FormValue("from")

		if r.Method == "POST" {
			f.username = r.FormValue("username")
			f.password = r.FormValue("password")

			s := "SELECT user_id, password, active FROM user WHERE username = ?"
			row := db.QueryRow(s, f.username, f.password)

			var userid int64
			var hashedpwd string
			var active int
			err := row.Scan(&userid, &hashedpwd, &active)

			for {
				if err == sql.ErrNoRows {
					errmsg = "Incorrect username or password"
					break
				}
				if err != nil {
					errmsg = "A problem occured. Please try again."
					break
				}
				if !isCorrectPassword(f.password, hashedpwd) {
					errmsg = "Incorrect username or password"
					break
				}
				if active == 0 {
					errmsg = fmt.Sprintf("User '%s' is inactive.", f.username)
					break
				}

				loginUser(w, userid)

				http.Redirect(w, r, unescapeUrl(qfrom), http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		site := querySite(db)
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)

		getTemplate().ExecuteTemplate(w, "login", map[string]any{
			"Site":       site,
			"QFrom":      qfrom,
			"ErrMessage": errmsg,
			"Username":   f.username,
			"Password":   f.password,
		})

		printPageFoot(w)
	}
}

func logoutHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		c := http.Cookie{
			Name:     "userid",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   0,
		}
		http.SetCookie(w, &c)

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func isUsernameExists(db *sql.DB, username string) bool {
	s := "SELECT user_id FROM user WHERE username = ?"
	row := db.QueryRow(s, username)
	var userid int64
	err := row.Scan(&userid)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		return false
	}
	return true
}

func createaccountHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var errmsg string
		var f struct{ username, email, password, password2 string }

		login := getLoginUser(r, db)
		qfrom := r.FormValue("from")

		if r.Method == "POST" {
			f.username = r.FormValue("username")
			f.email = r.FormValue("email")
			f.password = r.FormValue("password")
			f.password2 = r.FormValue("password2")
			for {
				if f.password != f.password2 {
					errmsg = "re-entered password doesn't match"
					f.password = ""
					f.password2 = ""
					break
				}
				if isUsernameExists(db, f.username) {
					errmsg = fmt.Sprintf("username '%s' already exists", f.username)
					break
				}

				hashedPassword := hashPassword(f.password)
				s := "INSERT INTO user (username, password, active, email) VALUES (?, ?, ?, ?);"
				result, err := sqlexec(db, s, f.username, hashedPassword, 1, f.email)
				if err != nil {
					log.Printf("DB error creating user: %s\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}
				newid, err := result.LastInsertId()
				if err == nil {
					loginUser(w, newid)
				} else {
					// DB doesn't support getting newly added userid, so login manually.
					qfrom = "/login/"
				}

				http.Redirect(w, r, unescapeUrl(qfrom), http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		site := querySite(db)
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)

		fmt.Fprintf(w, "<section class=\"main\">\n")
		fmt.Fprintf(w, "<form class=\"simpleform\" action=\"/createaccount/?from=%s\" method=\"post\">\n", url.QueryEscape(qfrom))
		fmt.Fprintf(w, "<h1 class=\"heading\">Create Account</h1>")
		if errmsg != "" {
			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<p class=\"error\">%s</p>\n", errmsg)
			fmt.Fprintf(w, "</div>\n")
		}
		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"username\">username</label>\n")
		fmt.Fprintf(w, "<input id=\"username\" name=\"username\" type=\"text\" size=\"20\" maxlength=\"20\" value=\"%s\">\n", f.username)
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"email\">email</label>\n")
		fmt.Fprintf(w, "<input id=\"email\" name=\"email\" type=\"email\" size=\"20\" value=\"%s\">\n", f.email)
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"password\">password</label>\n")
		fmt.Fprintf(w, "<input id=\"password\" name=\"password\" type=\"password\" size=\"20\" value=\"%s\">\n", f.password)
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"password2\">re-enter password</label>\n")
		fmt.Fprintf(w, "<input id=\"password2\" name=\"password2\" type=\"password\" size=\"20\" value=\"%s\">\n", f.password2)
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<button class=\"submit\">create account</button>\n")
		fmt.Fprintf(w, "</div>\n")
		fmt.Fprintf(w, "</form>\n")
		fmt.Fprintf(w, "</section>\n")

		printPageFoot(w)
	}
}

func adminsetupHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var errmsg string
		var f struct {
			title    string
			gravityf float64
		}

		login := getLoginUser(r, db)
		if login.Userid != ADMIN_ID {
			http.Error(w, "admin user required", 401)
			return
		}

		site := querySite(db)
		f.title = site.Title
		f.gravityf = site.Gravityf
		if f.gravityf < 0 {
			f.gravityf = 0.0
		}

		qfrom := r.FormValue("from")

		if r.Method == "POST" {
			for {
				f.title = strings.TrimSpace(r.FormValue("title"))
				f.gravityf = atof(r.FormValue("gravityf"))
				if f.title == "" {
					errmsg = "Enter a site title"
					break
				}
				if f.gravityf < 0 {
					errmsg = "Enter a gravity factor (0.0 and above)"
					break
				}

				s := "INSERT OR REPLACE INTO site (site_id, title, desc, gravityf) VALUES (1, ?, ?, ?)"
				_, err := sqlexec(db, s, f.title, "", f.gravityf)
				if err != nil {
					fmt.Printf("adminsetup site update DB error (%s)\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				http.Redirect(w, r, unescapeUrl(qfrom), http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)

		fmt.Fprintf(w, "<section class=\"main\">\n")

		fmt.Fprintf(w, "<form class=\"simpleform mb-xl\" action=\"/adminsetup/?from=%s\" method=\"post\">\n", url.QueryEscape(qfrom))
		fmt.Fprintf(w, "<h1 class=\"heading\">Site Settings</h1>")
		if errmsg != "" {
			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<p class=\"error\">%s</p>\n", errmsg)
			fmt.Fprintf(w, "</div>\n")
		}
		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"title\">site title</label>\n")
		fmt.Fprintf(w, "<input id=\"title\" name=\"title\" type=\"text\" size=\"30\" maxlength=\"50\" value=\"%s\">\n", escape(f.title))
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"gravityf\">gravity factor</label>\n")
		if f.gravityf >= 0 {
			fmt.Fprintf(w, "<input id=\"gravityf\" name=\"gravityf\" type=\"number\" step=\"0.001\" min=\"0\" size=\"5\" value=\"%.2f\">\n", f.gravityf)
		} else {
			fmt.Fprintf(w, "<input id=\"gravityf\" name=\"gravityf\" type=\"number\" step=\"0.001\" min=\"0\" size=\"5\" value=\"\">\n")
		}
		fmt.Fprintf(w, "<p class=\"text-sm text-fade-2 text-italic mt-xs\">\n")
		fmt.Fprintf(w, "points = num_votes / (hours_since_submission + 2) ^ gravity_factor.<br>The gravity_factor determines how quickly points decrease as time passes.\n")
		fmt.Fprintf(w, "</p>\n")
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<button class=\"submit\">submit</button>\n")
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "</form>\n")

		// Categories
		fmt.Fprintf(w, "<h1 class=\"heading mb-sm\">Categories</h1>\n")
		fmt.Fprintf(w, "<ul class=\"vertical-list mb-xl\">\n")
		fmt.Fprintf(w, "  <li><a class=\"text-fade-2 text-xs\" href=\"/createcat/?from=%s\">create new category</a></li>\n", url.QueryEscape("/adminsetup/"))
		var cat Cat
		s := "SELECT cat_id, name FROM cat ORDER BY cat_id"
		rows, _ := db.Query(s)
		for rows.Next() {
			rows.Scan(&cat.Catid, &cat.Name)
			fmt.Fprintf(w, "<li>\n")
			fmt.Fprintf(w, "  <div>%s</div>\n", escape(cat.Name))
			fmt.Fprintf(w, "  <ul class=\"line-menu text-fade-2 text-xs\">\n")
			fmt.Fprintf(w, "    <li><a href=\"/editcat?catid=%d&from=%s\">edit</a></li>\n", cat.Catid, url.QueryEscape("/adminsetup/"))
			if cat.Catid != 1 {
				fmt.Fprintf(w, "    <li><a href=\"/delcat?catid=%d&from=%s\">delete</a></li>\n", cat.Catid, url.QueryEscape("/adminsetup/"))
			}
			fmt.Fprintf(w, "  </ul>\n")
			fmt.Fprintf(w, "</li>\n")
		}
		fmt.Fprintf(w, "</ul>\n")

		// Users
		fmt.Fprintf(w, "<h1 class=\"heading mb-sm\">Users</h1>\n")
		s = "SELECT user_id, username, active, email FROM user ORDER BY username"
		rows, err := db.Query(s)
		if handleDbErr(w, err, "adminsetuphandler") {
			return
		}

		var u User
		fmt.Fprintf(w, "<ul class=\"vertical-list mb-xl\">\n")
		for rows.Next() {
			rows.Scan(&u.Userid, &u.Username, &u.Active, &u.Email)
			fmt.Fprintf(w, "<li>\n")
			if u.Active {
				fmt.Fprintf(w, "<div>%s</div>\n", escape(u.Username))
			} else {
				fmt.Fprintf(w, "<div class=\"text-fade-2\">(%s)</div>\n", escape(u.Username))
			}

			fmt.Fprintf(w, "<ul class=\"line-menu text-fade-2 text-xs\">\n")
			fmt.Fprintf(w, "  <li><a href=\"/edituser?userid=%d&from=%s\">edit</a>\n", u.Userid, url.QueryEscape("/adminsetup/"))
			if u.Userid != ADMIN_ID {
				if u.Active {
					fmt.Fprintf(w, "  <li><a href=\"/activateuser?userid=%d&setactive=0&from=%s\">deactivate</a>\n", u.Userid, url.QueryEscape("/adminsetup/"))
				} else {
					fmt.Fprintf(w, "  <li><a href=\"/activateuser?userid=%d&setactive=1&from=%s\">activate</a>\n", u.Userid, url.QueryEscape("/adminsetup/"))
				}
			}
			fmt.Fprintf(w, "</ul>\n")

			fmt.Fprintf(w, "</li>\n")
		}
		fmt.Fprintf(w, "</ul>\n")

		fmt.Fprintf(w, "</section>\n")
		printPageFoot(w)
	}
}

func usersetupHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		login := getLoginUser(r, db)
		if !validateLogin(w, login) {
			return
		}

		w.Header().Set("Content-Type", "text/html")
		site := querySite(db)
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)
		fmt.Fprintf(w, "<section class=\"main\">\n")

		fmt.Fprintf(w, "<p class=\"\"><a href=\"/edituser?userid=%d&from=%s\">Edit Account</a></p>\n", login.Userid, url.QueryEscape("/usersetup/"))
		fmt.Fprintf(w, "<p class=\"mt-base\"><a href=\"/edituser?userid=%d&setpwd=1&from=%s\">Set Password</a></p>\n", login.Userid, url.QueryEscape("/usersetup/"))

		fmt.Fprintf(w, "</section>\n")
		printPageFoot(w)
	}
}

func edituserHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var errmsg string
		var f struct{ username, email, password, password2 string }

		qfrom := r.FormValue("from")
		qsetpwd := r.FormValue("setpwd") // ?setpwd=1 to prompt for new password
		quserid := idtoi(r.FormValue("userid"))
		if quserid == -1 {
			log.Printf("edit user: no userid\n")
			http.Error(w, "missing userid parameter", 401)
			return
		}

		login := getLoginUser(r, db)
		if login.Userid != ADMIN_ID && login.Userid != quserid {
			log.Printf("edit user: admin or self user not logged in\n")
			http.Error(w, "admin or self user required", 401)
			return
		}

		u := queryUser(db, quserid)
		if u.Userid == -1 {
			log.Printf("edit user: userid %d doesn't exist\n", quserid)
			http.Error(w, "user doesn't exist", 401)
			return
		}

		f.username = u.Username
		f.email = u.Email

		if r.Method == "POST" {
			f.username = strings.TrimSpace(r.FormValue("username"))
			f.email = r.FormValue("email")

			for {
				// If username was changed,
				// make sure the new username hasn't been taken yet.
				if f.username != u.Username && isUsernameExists(db, f.username) {
					errmsg = fmt.Sprintf("username '%s' already exists", f.username)
					break
				}

				if f.username == "" {
					errmsg = "Please enter a username."
					break
				}

				var err error
				if qsetpwd == "" {
					s := "UPDATE user SET username = ?, email = ? WHERE user_id = ?"
					_, err = sqlexec(db, s, f.username, f.email, quserid)
				} else {
					// ?setpwd=1 to set new password
					f.password = r.FormValue("password")
					f.password2 = r.FormValue("password2")
					if f.password != f.password2 {
						errmsg = "re-entered password doesn't match"
						f.password = ""
						f.password2 = ""
						break
					}
					hashedPassword := hashPassword(f.password)
					s := "UPDATE user SET password = ? WHERE user_id = ?"
					_, err = sqlexec(db, s, hashedPassword, quserid)
				}
				if err != nil {
					log.Printf("DB error updating user: %s\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				http.Redirect(w, r, unescapeUrl(qfrom), http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		site := querySite(db)
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)

		fmt.Fprintf(w, "<div class=\"main\">\n")
		fmt.Fprintf(w, "<section class=\"main-content\">\n")
		fmt.Fprintf(w, "<form class=\"simpleform\" action=\"/edituser/?userid=%d&setpwd=%s&from=%s\" method=\"post\">\n", quserid, qsetpwd, url.QueryEscape(qfrom))
		fmt.Fprintf(w, "<h1 class=\"heading\">Edit User</h1>")
		if errmsg != "" {
			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<p class=\"error\">%s</p>\n", errmsg)
			fmt.Fprintf(w, "</div>\n")
		}
		// ?setpwd=1 to set new password
		if qsetpwd != "" {
			fmt.Fprintf(w, "<div class=\"control displayonly\">\n")
			fmt.Fprintf(w, "<label for=\"username\">username</label>\n")
			fmt.Fprintf(w, "<input id=\"username\" name=\"username\" type=\"text\" size=\"20\" maxlength=\"20\" value=\"%s\" readonly>\n", f.username)
			fmt.Fprintf(w, "</div>\n")

			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<label for=\"password\">password</label>\n")
			fmt.Fprintf(w, "<input id=\"password\" name=\"password\" type=\"password\" size=\"30\" value=\"%s\">\n", f.password)
			fmt.Fprintf(w, "</div>\n")

			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<label for=\"password2\">re-enter password</label>\n")
			fmt.Fprintf(w, "<input id=\"password2\" name=\"password2\" type=\"password\" size=\"30\" value=\"%s\">\n", f.password2)
			fmt.Fprintf(w, "</div>\n")
		} else {
			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<label for=\"username\">username</label>\n")
			fmt.Fprintf(w, "<input id=\"username\" name=\"username\" type=\"text\" size=\"20\" maxlength=\"20\" value=\"%s\">\n", f.username)
			fmt.Fprintf(w, "</div>\n")

			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<label for=\"email\">email</label>\n")
			fmt.Fprintf(w, "<input id=\"email\" name=\"email\" type=\"email\" size=\"20\" value=\"%s\">\n", f.email)
			fmt.Fprintf(w, "</div>\n")
		}

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<button class=\"submit\">update user</button>\n")
		fmt.Fprintf(w, "</div>\n")
		fmt.Fprintf(w, "</form>\n")
		fmt.Fprintf(w, "</section>\n")

		fmt.Fprintf(w, "</div>\n")
		printPageFoot(w)
	}
}

func activateuserHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var errmsg string

		qfrom := r.FormValue("from")
		qsetactive := atoi(r.FormValue("setactive"))
		if qsetactive != 0 && qsetactive != 1 {
			log.Printf("activate user: setactive should be 0 or 1\n")
			http.Error(w, "missing setactive parameter", 401)
			return
		}
		quserid := idtoi(r.FormValue("userid"))
		if quserid == -1 {
			log.Printf("activate user: no userid\n")
			http.Error(w, "missing userid parameter", 401)
			return
		}

		login := getLoginUser(r, db)
		if login.Userid != ADMIN_ID {
			log.Printf("activate user: admin not logged in\n")
			http.Error(w, "admin user required", 401)
			return
		}

		u := queryUser(db, quserid)
		if u.Userid == -1 {
			log.Printf("activate user: userid %d doesn't exist\n", quserid)
			http.Error(w, "user doesn't exist", 401)
			return
		}

		if r.Method == "POST" {
			for {
				s := "UPDATE user SET active = ? WHERE user_id = ?"
				_, err := sqlexec(db, s, qsetactive, quserid)
				if err != nil {
					log.Printf("DB error updating user.active: %s\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				http.Redirect(w, r, unescapeUrl(qfrom), http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		site := querySite(db)
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)

		fmt.Fprintf(w, "<div class=\"main\">\n")
		fmt.Fprintf(w, "<section class=\"main-content\">\n")
		fmt.Fprintf(w, "<form class=\"simpleform\" action=\"/activateuser/?userid=%d&setactive=%d&from=%s\" method=\"post\">\n", quserid, qsetactive, url.QueryEscape(qfrom))
		if qsetactive == 0 {
			fmt.Fprintf(w, "<h1 class=\"heading\">Deactivate User</h1>")
		} else {
			fmt.Fprintf(w, "<h1 class=\"heading\">Activate User</h1>")
		}
		if errmsg != "" {
			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<p class=\"error\">%s</p>\n", errmsg)
			fmt.Fprintf(w, "</div>\n")
		}
		fmt.Fprintf(w, "<div class=\"control displayonly\">\n")
		fmt.Fprintf(w, "<label for=\"username\">username</label>\n")
		fmt.Fprintf(w, "<input id=\"username\" name=\"username\" type=\"text\" size=\"20\" maxlength=\"20\" readonly value=\"%s\">\n", u.Username)
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		if qsetactive == 0 {
			fmt.Fprintf(w, "<button class=\"submit\">deactivate user</button>\n")
		} else {
			fmt.Fprintf(w, "<button class=\"submit\">activate user</button>\n")
		}
		fmt.Fprintf(w, "</div>\n")
		fmt.Fprintf(w, "</form>\n")
		fmt.Fprintf(w, "</section>\n")

		fmt.Fprintf(w, "</div>\n")
		printPageFoot(w)
	}
}

func getCountUnit(e *Entry, nchildren int) string {
	if e.Thing == SUBMISSION {
		if nchildren == 1 {
			return "comment"
		} else {
			return "comments"
		}
	} else {
		if nchildren == 1 {
			return "reply"
		} else {
			return "replies"
		}
	}
}

func getPointCountUnit(points int) string {
	if points == 1 {
		return "point"
	}
	return "points"
}

func printUpvote(w http.ResponseWriter, nvotes int, selfvote int) {
	var selfvoteclass string
	if selfvote == 1 {
		selfvoteclass = "selfvote "
	}
	fmt.Fprintf(w, "<a class=\"upvote %s mx-auto\">\n", selfvoteclass)
	fmt.Fprintf(w, "<svg viewbox=\"0 0 100 100\">\n")
	fmt.Fprintf(w, "  <polygon points=\"50 15, 100 100, 0 100\"/>\n")
	fmt.Fprintf(w, "</svg>\n")
	fmt.Fprintf(w, "</a>\n")
	fmt.Fprintf(w, "<div class=\"votectr mx-auto text-fade-2 text-sm\">%d</div>\n", nvotes)
}

func printCommentMarker(w http.ResponseWriter) {
	fmt.Fprintf(w, "<svg viewbox=\"0 0 100 100\" class=\"upvote\">\n")
	fmt.Fprintf(w, "  <rect x=\"25\" y=\"25\" width=\"40\" height=\"40\"/>\n")
	fmt.Fprintf(w, "</svg>\n")
}

func printSubmissionEntry(w http.ResponseWriter, r *http.Request, db *sql.DB, qi *QIndex, e *Entry, tt []string, submitter *User, login *User, ncomments, totalvotes int, selfvote int, points float64, showBody bool) {
	screatedt := parseIsoDate(e.Createdt)
	itemurl := createItemUrl(e.Entryid)
	entryurl := e.Url
	if entryurl == "" {
		entryurl = itemurl
	}

	tokparms := fmt.Sprintf("%d:%d", e.Entryid, login.Userid)
	fmt.Fprintf(w, "<section class=\"entry\" data-votetok=\"%s\">\n", encryptString(tokparms))
	fmt.Fprintf(w, "<div class=\"col0\">\n")
	printUpvote(w, totalvotes, selfvote)
	fmt.Fprintf(w, "</div>\n")

	fmt.Fprintf(w, "<div class=\"col1\">\n")
	fmt.Fprintf(w, "<div class=\"mb-xs text-lg\">\n")
	fmt.Fprintf(w, "  <a class=\"no-underline\" href=\"%s\">%s</a>\n", entryurl, escape(normalizeTitle(e.Title)))

	// url host
	if e.Url != "" {
		urllink, err := url.Parse(e.Url)
		urlhostname := strings.TrimPrefix(urllink.Hostname(), "www.")
		if err == nil {
			fmt.Fprintf(w, " <span class=\"text-fade-2 text-sm\">(%s)</span>\n", escape(urlhostname))
		}
	}
	// tags
	for _, t := range tt {
		fmt.Fprintf(w, " <span class=\"tag-pill text-fade-2\"><a class=\"no-underline\"href=\"/?cat=%d&tag=%s&latest=%s\">%s</a></span>\n", qi.Cat, url.QueryEscape(t), qi.Latest, escape(t))
	}

	fmt.Fprintf(w, "</div>\n")
	fmt.Fprintf(w, "<ul class=\"line-menu byline\">\n")
	npoints := int(math.Floor(points))
	fmt.Fprintf(w, "  <li>%d %s</li>\n", npoints, getPointCountUnit(npoints))
	fmt.Fprintf(w, "  <li><a href=\"/?cat=%d&username=%s&latest=%s\">%s</a></li>\n", qi.Cat, url.QueryEscape(submitter.Username), qi.Latest, url.QueryEscape(submitter.Username))
	if login.Userid == ADMIN_ID || submitter.Userid == login.Userid {
		fmt.Fprintf(w, "  <li><a class=\"\" href=\"/edit/?id=%d&from=%s\">edit</a></li>\n", e.Entryid, url.QueryEscape(r.RequestURI))
	}
	if login.Userid == ADMIN_ID || submitter.Userid == login.Userid {
		fromurl := r.RequestURI
		if strings.Contains(fromurl, "/item") {
			fromurl = "/"
		}
		fmt.Fprintf(w, "  <li><a class=\"\" href=\"/del/?id=%d&from=%s\">delete</a></li>\n", e.Entryid, url.QueryEscape(fromurl))
	}
	fmt.Fprintf(w, "  <li>%s</li>\n", screatedt)
	fmt.Fprintf(w, "  <li><a href=\"%s\">%d %s</a></li>\n", itemurl, ncomments, getCountUnit(e, ncomments))
	fmt.Fprintf(w, "</ul>\n")

	if showBody {
		fmt.Fprintf(w, "<div class=\"content mt-base mb-base\">\n")
		//e.Body = parseTextLinks(e.Body)
		e.Body = parseMarkdown(e.Body)
		fmt.Fprintf(w, e.Body)
		fmt.Fprintf(w, "</div>\n")
	}

	fmt.Fprintf(w, "</div>\n") // col1
	fmt.Fprintf(w, "</section>\n")
}

func printCommentEntry(w http.ResponseWriter, db *sql.DB, e *Entry, u *User, parent *Entry, ncomments int) {
	screatedt := parseIsoDate(e.Createdt)
	itemurl := createItemUrl(e.Entryid)
	entryurl := e.Url
	if entryurl == "" {
		entryurl = itemurl
	}

	fmt.Fprintf(w, "<section class=\"entry\">\n")
	fmt.Fprintf(w, "<div class=\"col0-comment\">\n")
	printCommentMarker(w)
	fmt.Fprintf(w, "</div>\n")

	fmt.Fprintf(w, "<div class=\"col1\">\n")
	fmt.Fprintf(w, "<ul class=\"line-menu byline\">\n")
	fmt.Fprintf(w, "  <li><a href=\"#\">%s</a></li>\n", escape(u.Username))
	fmt.Fprintf(w, "  <li>%s</li>\n", screatedt)
	fmt.Fprintf(w, "  <li><a href=\"%s\">%d %s</a></li>\n", itemurl, ncomments, getCountUnit(e, ncomments))

	if parent != nil {
		parenturl := createItemUrl(parent.Entryid)
		fmt.Fprintf(w, "  <li><a href=\"%s\">parent</a></li>\n", parenturl)
	}

	root, err := queryRootEntry(db, e.Entryid)
	if err == nil {
		rooturl := createItemUrl(root.Entryid)
		fmt.Fprintf(w, "  <li>on: <a href=\"%s\">%s</a></li>\n", rooturl, escape(normalizeTitle(root.Title)))
	} else {
		log.Printf("DB error querying root entry (%s)\n", err)
	}
	fmt.Fprintf(w, "</ul>\n")

	fmt.Fprintf(w, "<div class=\"content mt-base mb-base\">\n")
	//e.Body = parseTextLinks(e.Body)
	e.Body = parseMarkdown(e.Body)
	fmt.Fprintf(w, e.Body)
	fmt.Fprintf(w, "</div>\n")

	fmt.Fprintf(w, "</div>\n") // col1
	fmt.Fprintf(w, "</section>\n")
}

func printComment(w http.ResponseWriter, db *sql.DB, e *Entry, u *User, uparent *User, level int) {
	screatedt := parseIsoDate(e.Createdt)
	itemurl := createItemUrl(e.Entryid)
	entryurl := e.Url
	if entryurl == "" {
		entryurl = itemurl
	}

	// Limit the indents to maxindent
	maxindent := 1
	nindent := level
	if nindent > maxindent {
		nindent = maxindent
	}
	fmt.Fprintf(w, "<section class=\"entry\" style=\"padding-left: %drem\">\n", nindent*2)

	fmt.Fprintf(w, "<div class=\"col0-comment\">\n")
	printCommentMarker(w)
	fmt.Fprintf(w, "</div>\n")

	fmt.Fprintf(w, "<div class=\"col1\">\n")
	fmt.Fprintf(w, "  <p class=\"byline mb-xs\">%s <a href=\"%s\">%s</a></p>\n", escape(u.Username), itemurl, screatedt)

	fmt.Fprintf(w, "  <div class=\"content mt-xs mb-xs\">\n")
	body := e.Body
	if level >= 1 {
		//		fmt.Fprintf(w, "<span class=\"mention\">@%s</span> ", uparent.Username)
		body = fmt.Sprintf("***@%s*** %s", escape(uparent.Username), e.Body)
	}
	//body = parseTextLinks(body)
	body = parseMarkdown(body)
	fmt.Fprintf(w, body)
	fmt.Fprintf(w, "</div>\n")

	fmt.Fprintf(w, "  <p class=\"text-xs mb-base\"><a href=\"%s\">reply</a></p>\n", createItemUrl(e.Entryid))
	fmt.Fprintf(w, "</div>\n") // col1

	fmt.Fprintf(w, "</section>\n")
}

func indexHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		login := getLoginUser(r, db)
		site := querySite(db)

		qlatest := r.FormValue("latest")
		qusername := r.FormValue("username")
		qcat := idtoi(r.FormValue("cat"))
		if qcat == -1 {
			qcat = 0
		}
		qtag := r.FormValue("tag")
		qoffset := atoi(r.FormValue("offset"))
		if qoffset <= 0 {
			qoffset = 0
		}
		qlimit := atoi(r.FormValue("limit"))
		if qlimit <= 0 {
			qlimit = SETTINGS_LIMIT
		}

		w.Header().Set("Content-Type", "text/html")
		printPageHead(w, site, true)

		qi := &QIndex{
			Latest:   qlatest,
			Username: qusername,
			Cat:      qcat,
			Tag:      qtag,
		}
		printPageNav(w, login, site, qi)

		fmt.Fprintf(w, "<section class=\"main\">\n")

		// Inner heading
		fmt.Fprintf(w, "<div class=\"flex justify-start mb-base\">\n")
		fmt.Fprintf(w, "  <ul class=\"line-menu mr-2\">\n")
		if qcat <= 0 {
			fmt.Fprintf(w, "<li><a class=\"no-underline text-bold\" href=\"/?cat=0\">All</a></li>\n")
		} else {
			fmt.Fprintf(w, "<li><a class=\"no-underline\" href=\"/?cat=0\">All</a></li>\n")
		}
		var cat Cat
		s := "SELECT cat_id, name FROM cat ORDER BY cat_id"
		rows, _ := db.Query(s)
		for rows.Next() {
			rows.Scan(&cat.Catid, &cat.Name)
			url := fmt.Sprintf("/?cat=%d", cat.Catid)
			if cat.Catid == qcat {
				fmt.Fprintf(w, "<li><a class=\"no-underline text-bold\" href=\"%s\">%s</a></li>\n", url, escape(cat.Name))
				continue
			}
			fmt.Fprintf(w, "<li><a class=\"no-underline\" href=\"%s\">%s</a></li>\n", url, escape(cat.Name))
		}
		fmt.Fprintf(w, "  </ul>\n")

		fmt.Fprintf(w, "  <ul class=\"list-none\">\n")
		if qusername != "" {
			fmt.Fprintf(w, "    <li class=\"inline tag-pill mr-1\">%s</li>\n", escape(qusername))
		}
		if qtag != "" {
			fmt.Fprintf(w, "    <li class=\"inline tag-pill mr-1\">%s</li>\n", escape(qtag))
		}
		fmt.Fprintf(w, "  </ul>\n")
		fmt.Fprintf(w, "</div>\n")

		orderby := "points DESC, e.createdt DESC"
		if qlatest != "" {
			orderby = "e.createdt DESC"
		}

		var qq []interface{}
		qq = append(qq, site.Gravityf, login.Userid)
		where := "thing = 0"
		join := `LEFT OUTER JOIN user u ON e.user_id = u.user_id 
LEFT OUTER JOIN totalvotes ON e.entry_id = totalvotes.entry_id 
LEFT OUTER JOIN entryvote ev ON ev.entry_id = e.entry_id AND ev.user_id = ?`

		if qcat > 0 {
			join += " INNER JOIN entrycat ec ON e.entry_id = ec.entry_id AND ec.cat_id = ?"
			qq = append(qq, qcat)
		}
		if qtag != "" {
			join += " INNER JOIN entrytag et ON e.entry_id = et.entry_id AND et.tag = ?"
			qq = append(qq, qtag)
		}
		if qusername != "" {
			where += " AND u.Username = ?"
			qq = append(qq, qusername)
		}
		qq = append(qq, qlimit, qoffset)

		s = fmt.Sprintf(`SELECT e.entry_id, e.title, e.url, e.createdt, 
IFNULL(u.user_id, 0), IFNULL(u.username, ''),  
(SELECT COUNT(*) FROM entry AS child WHERE child.parent_id = e.entry_id) AS ncomments, 
IFNULL(totalvotes.votes, 0),
CASE WHEN ev.entry_id IS NOT NULL THEN 1 ELSE 0 END, 
calculate_points(IFNULL(totalvotes.votes, 0), e.createdt, ?) AS points
FROM entry AS e 
 %s 
WHERE %s 
ORDER BY %s 
LIMIT ? OFFSET ?`, join, where, orderby)
		rows, err := db.Query(s, qq...)
		if handleDbErr(w, err, "indexhandler") {
			return
		}

		fmt.Fprintf(w, "<ul class=\"vertical-list\">\n")
		var u User
		var e Entry
		var ncomments int
		var totalvotes int
		var selfvote int
		var points float64
		nrows := 0
		for rows.Next() {
			rows.Scan(&e.Entryid, &e.Title, &e.Url, &e.Createdt, &u.Userid, &u.Username, &ncomments, &totalvotes, &selfvote, &points)
			fmt.Fprintf(w, "<li>\n")

			tt, _ := queryEntryTags(db, e.Entryid)
			printSubmissionEntry(w, r, db, qi, &e, tt, &u, login, ncomments, totalvotes, selfvote, points, false)
			fmt.Fprintf(w, "</li>\n")
			nrows++
		}
		fmt.Fprintf(w, "</ul>\n")

		baseurl := fmt.Sprintf("/?username=%s&cat=%s&tag=%s&latest=%s", qusername, qcat, qtag, qlatest)
		printPagingNav(w, baseurl, qoffset, qlimit, nrows)
		fmt.Fprintf(w, "</section>\n")
		printPageFoot(w)
	}
}

func printPagingNav(w http.ResponseWriter, baseurl string, offset, limit, nrows int) {
	fmt.Fprintf(w, "<div class=\"flex-row text-sm text-fade-2 text-italic mt-xl\">\n")
	if offset > 0 {
		prevOffset := offset - limit
		if prevOffset < 0 {
			prevOffset = 0
		}
		prevLink := fmt.Sprintf("%s&offset=%d&limit=%d", baseurl, prevOffset, limit)
		fmt.Fprintf(w, "  <p><a href=\"%s\">Previous</a></p>\n", prevLink)
	} else {
		fmt.Fprintf(w, "  <p></p>\n")
	}
	if nrows == limit {
		moreLink := fmt.Sprintf("%s&offset=%d&limit=%d", baseurl, offset+limit, limit)
		fmt.Fprintf(w, "  <p><a href=\"%s\">More</a></p>\n", moreLink)
	}
	fmt.Fprintf(w, "</div>\n")
}

func validateIdParm(w http.ResponseWriter, id int64) bool {
	if id == -1 {
		http.Error(w, "Not found.", 404)
		return false
	}
	return true
}

func queryRootEntry(db *sql.DB, entryid int64) (*Entry, error) {
	nIteration := 0
	for {
		var e, p Entry
		s := `SELECT e.entry_id, e.thing, e.title, e.url, e.body, e.createdt, 
IFNULL(p.entry_id, 0), IFNULL(p.thing, 0), IFNULL(p.title, ''), IFNULL(p.url, ''), IFNULL(p.body, ''), IFNULL(p.createdt, '') 
FROM entry e 
LEFT OUTER JOIN entry p ON e.parent_id = p.entry_id 
WHERE e.entry_id = ?`
		row := db.QueryRow(s, entryid)
		err := row.Scan(&e.Entryid, &e.Thing, &e.Title, &e.Url, &e.Body, &e.Createdt,
			&p.Entryid, &p.Thing, &p.Title, &p.Url, &p.Body, &p.Createdt)
		if err != nil {
			return nil, err
		}

		// If no parent, e is the root
		if p.Entryid == 0 {
			return &e, nil
		}

		// Set a limit to number of iterations in case there's a circular reference
		nIteration++
		if nIteration > 100 {
			return &e, nil
		}

		entryid = p.Entryid
	}
}

func handleDbErr(w http.ResponseWriter, err error, sfunc string) bool {
	if err == sql.ErrNoRows {
		http.Error(w, "Not found.", 404)
		return true
	}
	if err != nil {
		log.Printf("%s: database error (%s)\n", sfunc, err)
		http.Error(w, "Server database error.", 500)
		return true
	}
	return false
}

func validateLogin(w http.ResponseWriter, login *User) bool {
	if login.Userid == -1 {
		http.Error(w, "Not logged in.", 401)
		return false
	}
	if !login.Active {
		http.Error(w, "Not an active user.", 401)
		return false
	}
	return true
}

func createItemUrl(id int64) string {
	return fmt.Sprintf("/item/?id=%d", id)
}

func itemHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		login := getLoginUser(r, db)
		site := querySite(db)

		qentryid := idtoi(r.FormValue("id"))
		if !validateIdParm(w, qentryid) {
			return
		}

		var u User
		var e Entry
		var catid int64
		var p Entry // parent
		var ncomments int
		var totalvotes int
		var selfvote int
		var points float64

		s := `SELECT e.entry_id, e.thing, e.title, e.url, e.body, e.createdt, IFNULL(ec.cat_id, 0), 
IFNULL(p.entry_id, 0), IFNULL(p.thing, 0), IFNULL(p.title, ''), IFNULL(p.url, ''), IFNULL(p.body, ''), IFNULL(p.createdt, ''), 
IFNULL(u.user_id, 0), IFNULL(u.username, ''), IFNULL(u.active, 0), IFNULL(u.email, ''),  
(SELECT COUNT(*) FROM entry AS child WHERE child.parent_id = e.entry_id) AS ncomments, 
IFNULL(totalvotes.votes, 0) AS votes, 
CASE WHEN ev.entry_id IS NOT NULL THEN 1 ELSE 0 END, 
calculate_points(IFNULL(totalvotes.votes, 0), e.createdt, ?) AS points
FROM entry e 
LEFT OUTER JOIN user u ON e.user_id = u.user_id 
LEFT OUTER JOIN entrycat ec ON e.entry_id = ec.entry_id 
LEFT OUTER JOIN totalvotes ON e.entry_id = totalvotes.entry_id 
LEFT OUTER JOIN entryvote ev ON ev.entry_id = e.entry_id AND ev.user_id = ?
LEFT OUTER JOIN entry p ON e.parent_id = p.entry_id 
WHERE e.entry_id = ?`
		row := db.QueryRow(s, site.Gravityf, login.Userid, qentryid)
		err := row.Scan(&e.Entryid, &e.Thing, &e.Title, &e.Url, &e.Body, &e.Createdt, &catid,
			&p.Entryid, &p.Thing, &p.Title, &p.Url, &p.Body, &p.Createdt,
			&u.Userid, &u.Username, &u.Active, &u.Email,
			&ncomments, &totalvotes, &selfvote, &points)
		if handleDbErr(w, err, "itemhandler") {
			return
		}

		itemurl := createItemUrl(e.Entryid)

		var errmsg string
		var comment Entry
		if r.Method == "POST" {
			if !validateLogin(w, login) {
				return
			}

			for {
				comment.Body = strings.TrimSpace(r.FormValue("commentbody"))
				if comment.Body == "" {
					errmsg = "Please enter a comment."
					break
				}

				comment.Body = strings.ReplaceAll(comment.Body, "\r", "") // CRLF => CR
				comment.Createdt = time.Now().Format(time.RFC3339)

				s := "INSERT INTO entry (thing, parent_id, title, body, createdt, user_id) VALUES (?, ?, ?, ?, ?, ?)"
				_, err := sqlexec(db, s, COMMENT, e.Entryid, "", comment.Body, comment.Createdt, login.Userid)
				if err != nil {
					log.Printf("DB error creating comment (%s)\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				http.Redirect(w, r, itemurl, http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		printPageHead(w, site, true)
		printPageNav(w, login, site, nil)

		fmt.Fprintf(w, "<section class=\"main\">\n")
		if e.Thing == SUBMISSION {
			tt, _ := queryEntryTags(db, e.Entryid)
			qi := &QIndex{Cat: catid}
			printSubmissionEntry(w, r, db, qi, &e, tt, &u, login, ncomments, totalvotes, selfvote, points, true)
		} else if e.Thing == COMMENT {
			printCommentEntry(w, db, &e, &u, &p, ncomments)
		}

		fmt.Fprintf(w, "<form class=\"simpleform mb-2xl\" method=\"post\" action=\"%s\">\n", itemurl)
		if login.Userid == -1 || !login.Active {
			fmt.Fprintf(w, "<div class=\"control text-sm text-fade-2 text-italic\">\n")
			fmt.Fprintf(w, "<label><a href=\"/login/?from=%s\">Log in</a> to post a comment.</label>\n", url.QueryEscape(r.RequestURI))
			fmt.Fprintf(w, "</div>\n")
		} else {
			if errmsg != "" {
				fmt.Fprintf(w, "<div class=\"control\">\n")
				fmt.Fprintf(w, "<p class=\"error\">%s</p>\n", errmsg)
				fmt.Fprintf(w, "</div>\n")
			}
			fmt.Fprintf(w, "  <div class=\"control\">\n")
			fmt.Fprintf(w, "    <textarea id=\"commentbody\" name=\"commentbody\" rows=\"6\" cols=\"60\">%s</textarea>\n", comment.Body)
			fmt.Fprintf(w, "  </div>\n")
			fmt.Fprintf(w, "  <div class=\"control\">\n")
			fmt.Fprintf(w, "    <button class=\"submit\">add comment</button>\n")
			fmt.Fprintf(w, "  </div>\n")
		}
		fmt.Fprintf(w, "</form>\n")

		fmt.Fprintf(w, "<section class=\"entry-comments\">\n")
		printComments(w, db, e.Entryid, 0)
		fmt.Fprintf(w, "</section>\n")

		fmt.Fprintf(w, "</section>\n")
		printPageFoot(w)
	}
}

func printComments(w http.ResponseWriter, db *sql.DB, parentid int64, level int) {
	s := `SELECT e.entry_id, e.body, e.createdt, u.user_id, u.username, uparent.user_id, uparent.username  
FROM entry AS e 
LEFT OUTER JOIN user u ON e.user_id = u.user_id 
LEFT OUTER JOIN entry p ON e.parent_id = p.entry_id 
LEFT OUTER JOIN user uparent ON uparent.user_id = p.user_id
WHERE e.thing = 1 AND e.parent_id = ? 
ORDER BY e.entry_id`
	rows, err := db.Query(s, parentid)
	if handleDbErr(w, err, "printComments") {
		return
	}
	var reply Entry
	var u, uparent User
	for rows.Next() {
		rows.Scan(&reply.Entryid, &reply.Body, &reply.Createdt, &u.Userid, &u.Username, &uparent.Userid, &uparent.Username)

		printComment(w, db, &reply, &u, &uparent, level)
		printComments(w, db, reply.Entryid, level+1)
	}
}

func submitHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		login := getLoginUser(r, db)

		var e Entry
		var tags string

		catid := idtoi(r.FormValue("cat"))
		if catid == -1 {
			catid = 1
		}

		var errmsg string
		if r.Method == "POST" {
			if !validateLogin(w, login) {
				return
			}

		SUBMIT_FOR:
			for {
				e.Title = strings.TrimSpace(r.FormValue("title"))
				e.Url = strings.TrimSpace(r.FormValue("url"))
				e.Body = strings.TrimSpace(r.FormValue("body"))
				tags = strings.TrimSpace(r.FormValue("tags"))
				if e.Title == "" {
					errmsg = "Please enter a title."
					break
				}
				if e.Url == "" && e.Body == "" {
					errmsg = "Please enter a url or text writeup."
					break
				}

				e.Body = strings.ReplaceAll(e.Body, "\r", "") // CRLF => CR
				e.Createdt = time.Now().Format(time.RFC3339)

				s := "INSERT INTO entry (thing, title, url, body, createdt, user_id) VALUES (?, ?, ?, ?, ?, ?)"
				result, err := sqlexec(db, s, SUBMISSION, e.Title, e.Url, e.Body, e.Createdt, login.Userid)
				if err != nil {
					log.Printf("DB error creating submission (%s)\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}
				newid, err := result.LastInsertId()
				if err != nil {
					log.Printf("DB error getting LastInsertId() (%s)\n", err)
					http.Redirect(w, r, "/", http.StatusSeeOther)
					return
				}

				// Set category
				s = "INSERT INTO entrycat (entry_id, cat_id) VALUES (?, ?)"
				_, err = sqlexec(db, s, newid, catid)
				if err != nil {
					log.Printf("DB error setting submission category (%s)\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				// Add entry tags
				tt := strings.Split(tags, ",")
				for _, t := range tt {
					t = strings.TrimSpace(t)
					if t == "" {
						continue
					}
					s := "INSERT INTO entrytag (entry_id, tag) VALUES (?, ?)"
					_, err := sqlexec(db, s, newid, t)
					if err != nil {
						log.Printf("DB error adding tag (%s)\n", err)
						errmsg = "A problem occured. Please try again."
						break SUBMIT_FOR
					}
				}

				http.Redirect(w, r, createItemUrl(newid), http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		site := querySite(db)
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)
		fmt.Fprintf(w, "<section class=\"main\">\n")

		fmt.Fprintf(w, "<form class=\"simpleform mb-2xl\" method=\"post\" action=\"/submit/\">\n")
		if login.Userid == -1 || !login.Active {
			fmt.Fprintf(w, "<div class=\"control text-sm text-fade-2 text-italic\">\n")
			fmt.Fprintf(w, "<label><a href=\"/login/?from=%s\">Log in</a> to post a comment.</label>\n", url.QueryEscape(r.RequestURI))
			fmt.Fprintf(w, "</div>\n")

			fmt.Fprintf(w, "</form>\n")
			fmt.Fprintf(w, "</section>\n")
			printPageFoot(w)
			return
		}

		if errmsg != "" {
			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<p class=\"error\">%s</p>\n", errmsg)
			fmt.Fprintf(w, "</div>\n")
		}

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"title\">title</label>\n")
		fmt.Fprintf(w, "<input id=\"title\" name=\"title\" type=\"text\" size=\"60\" maxlength=\"256\" value=\"%s\">\n", escape(e.Title))
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"url\">url</label>\n")
		fmt.Fprintf(w, "<input id=\"url\" name=\"url\" type=\"text\" size=\"60\" value=\"%s\">\n", escape(e.Url))
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "  <div class=\"control\">\n")
		fmt.Fprintf(w, "    <label for=\"body\">text</label>\n")
		fmt.Fprintf(w, "    <textarea id=\"body\" name=\"body\" rows=\"6\" cols=\"60\">%s</textarea>\n", e.Body)
		fmt.Fprintf(w, "  </div>\n")

		fmt.Fprintf(w, "  <div class=\"control\">\n")
		fmt.Fprintf(w, "    <label for=\"cat\">category</label>\n")
		fmt.Fprintf(w, "    <select id=\"cat\" name=\"cat\">\n")
		var cat Cat
		s := "SELECT cat_id, name FROM cat ORDER BY cat_id"
		rows, err := db.Query(s)
		if handleDbErr(w, err, "submithandler") {
			return
		}
		for rows.Next() {
			rows.Scan(&cat.Catid, &cat.Name)
			if cat.Catid == catid {
				fmt.Fprintf(w, "<option value=\"%d\" selected>%s</option>\n", cat.Catid, escape(cat.Name))
				continue
			}
			fmt.Fprintf(w, "<option value=\"%d\">%s</option>\n", cat.Catid, escape(cat.Name))
		}
		fmt.Fprintf(w, "    </select>\n")
		fmt.Fprintf(w, "  </div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"tags\">tags</label>\n")
		fmt.Fprintf(w, "<input id=\"tags\" name=\"tags\" type=\"text\" size=\"60\" maxlength=\"256\" value=\"%s\">\n", escape(tags))
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "  <div class=\"control\">\n")
		fmt.Fprintf(w, "    <button class=\"submit\">submit</button>\n")
		fmt.Fprintf(w, "  </div>\n")

		fmt.Fprintf(w, "</form>\n")
		fmt.Fprintf(w, "</section>\n")
		printPageFoot(w)
	}
}

func delEntry(tx *sql.Tx, db *sql.DB, entryid int64) error {
	s := "DELETE FROM entry WHERE entry_id = ?"
	_, err := txexec(tx, s, entryid)
	if err != nil {
		return err
	}

	// Delete children of entry recursively.
	s = "SELECT entry_id FROM entry WHERE parent_id = ?"
	rows, err := db.Query(s, entryid)
	for rows.Next() {
		var childid int64
		rows.Scan(&childid)
		err = delEntry(tx, db, childid)
		if err != nil {
			return err
		}
	}

	return nil
}

func delHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		login := getLoginUser(r, db)

		qentryid := idtoi(r.FormValue("id"))
		if !validateIdParm(w, qentryid) {
			return
		}

		qfrom := r.FormValue("from")

		var e Entry
		s := "SELECT e.entry_id, e.thing, e.title, e.url, e.body, e.createdt, e.user_id FROM entry e WHERE e.entry_id = ?"
		row := db.QueryRow(s, qentryid)
		err := row.Scan(&e.Entryid, &e.Thing, &e.Title, &e.Url, &e.Body, &e.Createdt, &e.Userid)
		if handleDbErr(w, err, "itemhandler") {
			return
		}
		if login.Userid != ADMIN_ID && e.Userid != login.Userid {
			http.Error(w, "admin or entry submitter required", 401)
			return
		}

		var errmsg string
		if r.Method == "POST" {
			for {
				tx, err := db.Begin()
				if err != nil {
					log.Printf("DB error creating transaction (%s)\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				err = delEntry(tx, db, qentryid)
				if err != nil {
					log.Printf("DB error deleting entry (%s)\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				err = tx.Commit()
				if err != nil {
					log.Printf("DB error commiting transaction (%s)\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				http.Redirect(w, r, unescapeUrl(qfrom), http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		site := querySite(db)
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)
		fmt.Fprintf(w, "<section class=\"main\">\n")

		fmt.Fprintf(w, "<form class=\"simpleform mb-2xl\" method=\"post\" action=\"/del/?id=%d&from=%s\">\n", qentryid, url.QueryEscape(qfrom))
		if errmsg != "" {
			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<p class=\"error\">%s</p>\n", errmsg)
			fmt.Fprintf(w, "</div>\n")
		}

		fmt.Fprintf(w, "<div class=\"control displayonly\">\n")
		fmt.Fprintf(w, "<label for=\"title\">title</label>\n")
		fmt.Fprintf(w, "<input id=\"title\" name=\"title\" type=\"text\" size=\"60\" maxlength=\"256\" value=\"%s\" readonly>\n", e.Title)
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control displayonly\">\n")
		fmt.Fprintf(w, "<label for=\"url\">url</label>\n")
		fmt.Fprintf(w, "<input id=\"url\" name=\"url\" type=\"text\" size=\"60\" value=\"%s\" readonly>\n", e.Url)
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "  <div class=\"control displayonly\">\n")
		fmt.Fprintf(w, "    <label for=\"body\">text</label>\n")
		fmt.Fprintf(w, "    <textarea id=\"body\" name=\"body\" rows=\"6\" cols=\"60\" readonly>%s</textarea>\n", e.Body)
		fmt.Fprintf(w, "  </div>\n")

		fmt.Fprintf(w, "  <div class=\"control\">\n")
		fmt.Fprintf(w, "    <button class=\"submit\">delete</button>\n")
		fmt.Fprintf(w, "  </div>\n")
		fmt.Fprintf(w, "</form>\n")

		fmt.Fprintf(w, "</section>\n")
		printPageFoot(w)
	}
}

func editHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		login := getLoginUser(r, db)
		qentryid := idtoi(r.FormValue("id"))
		if !validateIdParm(w, qentryid) {
			return
		}

		var e Entry
		var catid int64
		s := "SELECT e.entry_id, e.thing, e.title, e.url, e.body, e.createdt, e.user_id, IFNULL(ec.cat_id, 0) FROM entry e LEFT OUTER JOIN entrycat ec ON e.entry_id = ec.entry_id WHERE e.entry_id = ?"
		row := db.QueryRow(s, qentryid)
		err := row.Scan(&e.Entryid, &e.Thing, &e.Title, &e.Url, &e.Body, &e.Createdt, &e.Userid, &catid)
		if handleDbErr(w, err, "edithandler") {
			return
		}
		if login.Userid != ADMIN_ID && login.Userid != e.Userid {
			http.Error(w, "admin user or entry submitter required", 401)
			return
		}

		tt, err := queryEntryTags(db, qentryid)
		if err != nil {
			log.Printf("edithandler: database error (%s)\n", err)
			http.Error(w, "Server database error.", 500)
			return
		}
		tags := strings.Join(tt, ", ")

		var errmsg string
		if r.Method == "POST" {
		SUBMIT_FOR:
			for {
				e.Title = strings.TrimSpace(r.FormValue("title"))
				e.Url = strings.TrimSpace(r.FormValue("url"))
				e.Body = strings.TrimSpace(r.FormValue("body"))
				tags = strings.TrimSpace(r.FormValue("tags"))
				catid = idtoi(r.FormValue("cat"))
				if e.Title == "" {
					errmsg = "Please enter a title."
					break
				}
				if e.Url == "" && e.Body == "" {
					errmsg = "Please enter a url or text writeup."
					break
				}

				e.Body = strings.ReplaceAll(e.Body, "\r", "") // CRLF => CR
				e.Createdt = time.Now().Format(time.RFC3339)

				s := "UPDATE entry SET title = ?, url = ?, body = ? WHERE entry_id = ?"
				_, err = sqlexec(db, s, e.Title, e.Url, e.Body, qentryid)
				if err != nil {
					log.Printf("DB error updating submission (%s)\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				// Set category
				s = "UPDATE entrycat SET cat_id = ? WHERE entry_id = ?"
				_, err = sqlexec(db, s, catid, qentryid)
				if err != nil {
					log.Printf("DB error updating submission category (%s)\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				// Update entry tags
				s = "DELETE FROM entrytag WHERE entry_id = ?"
				_, err = sqlexec(db, s, qentryid)
				if err != nil {
					log.Printf("DB error deleting existing entry tags (%s)\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				tt := strings.Split(tags, ",")
				for _, t := range tt {
					t = strings.TrimSpace(t)
					if t == "" {
						continue
					}
					s := "INSERT INTO entrytag (entry_id, tag) VALUES (?, ?)"
					_, err := sqlexec(db, s, qentryid, t)
					if err != nil {
						log.Printf("DB error adding tag (%s)\n", err)
						errmsg = "A problem occured. Please try again."
						break SUBMIT_FOR
					}
				}

				http.Redirect(w, r, createItemUrl(qentryid), http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		site := querySite(db)
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)
		fmt.Fprintf(w, "<section class=\"main\">\n")

		fmt.Fprintf(w, "<form class=\"simpleform mb-2xl\" method=\"post\" action=\"/edit/?id=%d\">\n", qentryid)
		if errmsg != "" {
			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<p class=\"error\">%s</p>\n", errmsg)
			fmt.Fprintf(w, "</div>\n")
		}

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"title\">title</label>\n")
		fmt.Fprintf(w, "<input id=\"title\" name=\"title\" type=\"text\" size=\"60\" maxlength=\"256\" value=\"%s\">\n", escape(e.Title))
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"url\">url</label>\n")
		fmt.Fprintf(w, "<input id=\"url\" name=\"url\" type=\"text\" size=\"60\" value=\"%s\">\n", escape(e.Url))
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "  <div class=\"control\">\n")
		fmt.Fprintf(w, "    <label for=\"body\">text</label>\n")
		fmt.Fprintf(w, "    <textarea id=\"body\" name=\"body\" rows=\"6\" cols=\"60\">%s</textarea>\n", e.Body)
		fmt.Fprintf(w, "  </div>\n")

		fmt.Fprintf(w, "  <div class=\"control\">\n")
		fmt.Fprintf(w, "    <label for=\"cat\">category</label>\n")
		fmt.Fprintf(w, "    <select id=\"cat\" name=\"cat\">\n")
		var cat Cat
		s = "SELECT cat_id, name FROM cat ORDER BY cat_id"
		rows, err := db.Query(s)
		if handleDbErr(w, err, "submithandler") {
			return
		}
		for rows.Next() {
			rows.Scan(&cat.Catid, &cat.Name)
			if cat.Catid == catid {
				fmt.Fprintf(w, "<option value=\"%d\" selected>%s</option>\n", cat.Catid, escape(cat.Name))
				continue
			}
			fmt.Fprintf(w, "<option value=\"%d\">%s</option>\n", cat.Catid, escape(cat.Name))
		}
		fmt.Fprintf(w, "    </select>\n")
		fmt.Fprintf(w, "  </div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"tags\">tags</label>\n")
		fmt.Fprintf(w, "<input id=\"tags\" name=\"tags\" type=\"text\" size=\"60\" maxlength=\"256\" value=\"%s\">\n", escape(tags))
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "  <div class=\"control\">\n")
		fmt.Fprintf(w, "    <button class=\"submit\">submit</button>\n")
		fmt.Fprintf(w, "  </div>\n")
		fmt.Fprintf(w, "</form>\n")

		fmt.Fprintf(w, "</section>\n")
		printPageFoot(w)
	}
}

func decryptVoteTok(tok string) (int64, int64) {
	// When decrypted, tok takes the format: <entryid>:<userid>.
	// Ex. 123:12  where entryid = 123, userid = 12
	parms := decryptString(tok)
	sre := `^(\d+):(\d+)$`
	re := regexp.MustCompile(sre)
	matches := re.FindStringSubmatch(parms)
	if matches == nil {
		return -1, -1
	}

	entryid := idtoi(matches[1])
	userid := idtoi(matches[2])
	return entryid, userid
}

func voteHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		//todo authenticate request
		qtok := r.FormValue("tok")
		if qtok == "" {
			log.Printf("voteHandler: tok required\n")
			http.Error(w, "tok required", 401)
			return
		}

		entryid, userid := decryptVoteTok(qtok)
		if entryid == -1 || userid == -1 {
			log.Printf("voteHandler: invalid tok format\n")
			http.Error(w, "invalid tok format", 401)
			return
		}

		e, err := queryEntry(db, entryid)
		if err != nil {
			log.Printf("voteHandler: DB error (%s)\n", err)
			http.Error(w, "Server error", 500)
			return
		}
		if e == nil {
			log.Printf("voteHandler: entryid %d doesn't exist\n", entryid)
			http.Error(w, "entryid doesn't exist", 401)
			return
		}

		u := queryUser(db, userid)
		if u.Userid == -1 {
			log.Printf("voteHandler: userid %d doesn't exist\n", userid)
			http.Error(w, "user doesn't exist", 401)
			return
		}

		s := "INSERT OR REPLACE INTO entryvote (entry_id, user_id) VALUES (?, ?)"
		_, err = sqlexec(db, s, entryid, userid)
		if err != nil {
			log.Printf("voteHandler: DB error (%s)\n", err)
			http.Error(w, "Server error", 500)
			return
		}

		var votes int
		s = "SELECT IFNULL(totalvotes.votes, 0) AS votes FROM entry e LEFT OUTER JOIN totalvotes ON e.entry_id = totalvotes.entry_id WHERE e.entry_id = ?"
		row := db.QueryRow(s, entryid)
		err = row.Scan(&votes)
		if handleDbErr(w, err, "votehandler") {
			return
		}

		vr := VoteResult{
			Entryid:    entryid,
			Userid:     userid,
			TotalVotes: votes,
		}
		bs, _ := json.Marshal(vr)
		w.WriteHeader(200)
		w.Write(bs)
	}
}

func unvoteHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		//todo authenticate request
		qtok := r.FormValue("tok")
		if qtok == "" {
			log.Printf("unvoteHandler: tok required\n")
			http.Error(w, "tok required", 401)
			return
		}

		entryid, userid := decryptVoteTok(qtok)
		if entryid == -1 || userid == -1 {
			log.Printf("unvoteHandler: invalid tok format\n")
			http.Error(w, "invalid tok format", 401)
			return
		}

		e, err := queryEntry(db, entryid)
		if err != nil {
			log.Printf("unvoteHandler: DB error (%s)\n", err)
			http.Error(w, "Server error", 500)
			return
		}
		if e == nil {
			log.Printf("unvoteHandler: entryid %d doesn't exist\n", entryid)
			http.Error(w, "entryid doesn't exist", 401)
			return
		}

		u := queryUser(db, userid)
		if u.Userid == -1 {
			log.Printf("unvoteHandler: userid %d doesn't exist\n", userid)
			http.Error(w, "user doesn't exist", 401)
			return
		}

		s := "DELETE FROM entryvote WHERE entry_id = ? AND user_id = ?"
		_, err = sqlexec(db, s, entryid, userid)
		if err != nil {
			log.Printf("unvoteHandler: DB error (%s)\n", err)
			http.Error(w, "Server error", 500)
			return
		}

		var votes int
		s = "SELECT IFNULL(totalvotes.votes, 0) AS votes FROM entry e LEFT OUTER JOIN totalvotes ON e.entry_id = totalvotes.entry_id WHERE e.entry_id = ?"
		row := db.QueryRow(s, entryid)
		err = row.Scan(&votes)
		if handleDbErr(w, err, "unvotehandler") {
			return
		}

		vr := VoteResult{
			Entryid:    entryid,
			Userid:     userid,
			TotalVotes: votes,
		}
		bs, _ := json.Marshal(vr)
		w.WriteHeader(200)
		w.Write(bs)
	}
}

func createcatHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var errmsg string
		var cat Cat

		qfrom := r.FormValue("from")

		login := getLoginUser(r, db)
		if login.Userid != ADMIN_ID {
			log.Printf("create cat: admin not logged in\n")
			http.Error(w, "admin required", 401)
			return
		}

		if r.Method == "POST" {
			cat.Name = strings.TrimSpace(r.FormValue("name"))

			for {
				if cat.Name == "" {
					errmsg = "Please enter a category name."
					break
				}

				var err error
				s := "INSERT INTO cat (name) VALUES (?)"
				_, err = sqlexec(db, s, cat.Name)
				if err != nil {
					log.Printf("DB error creating cat: %s\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				http.Redirect(w, r, unescapeUrl(qfrom), http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		site := querySite(db)
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)

		fmt.Fprintf(w, "<div class=\"main\">\n")
		fmt.Fprintf(w, "<section class=\"main-content\">\n")
		fmt.Fprintf(w, "<form class=\"simpleform\" action=\"/createcat/?from=%s\" method=\"post\">\n", url.QueryEscape(qfrom))
		fmt.Fprintf(w, "<h1 class=\"heading\">Create Category</h1>")
		if errmsg != "" {
			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<p class=\"error\">%s</p>\n", errmsg)
			fmt.Fprintf(w, "</div>\n")
		}
		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"name\">category name</label>\n")
		fmt.Fprintf(w, "<input id=\"name\" name=\"name\" type=\"text\" size=\"20\" maxlength=\"20\" value=\"%s\">\n", escape(cat.Name))
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<button class=\"submit\">create category</button>\n")
		fmt.Fprintf(w, "</div>\n")
		fmt.Fprintf(w, "</form>\n")
		fmt.Fprintf(w, "</section>\n")

		fmt.Fprintf(w, "</div>\n")
		printPageFoot(w)
	}
}

func editcatHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var errmsg string

		qfrom := r.FormValue("from")
		qcatid := idtoi(r.FormValue("catid"))
		if qcatid == -1 {
			log.Printf("edit cat: no catid\n")
			http.Error(w, "missing catid parameter", 401)
			return
		}

		login := getLoginUser(r, db)
		if login.Userid != ADMIN_ID {
			log.Printf("edit cat: admin not logged in\n")
			http.Error(w, "admin required", 401)
			return
		}

		cat := queryCat(db, qcatid)
		if cat == nil {
			log.Printf("edit cat: catid %d doesn't exist\n", qcatid)
			http.Error(w, "cat doesn't exist", 401)
			return
		}

		if r.Method == "POST" {
			cat.Name = strings.TrimSpace(r.FormValue("name"))

			for {
				if cat.Name == "" {
					errmsg = "Please enter a category name."
					break
				}

				var err error
				s := "UPDATE cat SET name = ? WHERE cat_id = ?"
				_, err = sqlexec(db, s, cat.Name, qcatid)
				if err != nil {
					log.Printf("DB error updating cat: %s\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				http.Redirect(w, r, unescapeUrl(qfrom), http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		site := querySite(db)
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)

		fmt.Fprintf(w, "<div class=\"main\">\n")
		fmt.Fprintf(w, "<section class=\"main-content\">\n")
		fmt.Fprintf(w, "<form class=\"simpleform\" action=\"/editcat/?catid=%d&from=%s\" method=\"post\">\n", qcatid, url.QueryEscape(qfrom))
		fmt.Fprintf(w, "<h1 class=\"heading\">Edit Category</h1>")
		if errmsg != "" {
			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<p class=\"error\">%s</p>\n", errmsg)
			fmt.Fprintf(w, "</div>\n")
		}
		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<label for=\"name\">category name</label>\n")
		fmt.Fprintf(w, "<input id=\"name\" name=\"name\" type=\"text\" size=\"20\" maxlength=\"20\" value=\"%s\">\n", escape(cat.Name))
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<button class=\"submit\">update category</button>\n")
		fmt.Fprintf(w, "</div>\n")
		fmt.Fprintf(w, "</form>\n")
		fmt.Fprintf(w, "</section>\n")

		fmt.Fprintf(w, "</div>\n")
		printPageFoot(w)
	}
}

func delcatHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var errmsg string

		qfrom := r.FormValue("from")
		qcatid := idtoi(r.FormValue("catid"))
		if qcatid == -1 {
			log.Printf("del cat: no catid\n")
			http.Error(w, "missing catid parameter", 401)
			return
		}

		login := getLoginUser(r, db)
		if login.Userid != ADMIN_ID {
			log.Printf("del cat: admin not logged in\n")
			http.Error(w, "admin required", 401)
			return
		}

		cat := queryCat(db, qcatid)
		if cat == nil {
			log.Printf("del cat: catid %d doesn't exist\n", qcatid)
			http.Error(w, "cat doesn't exist", 401)
			return
		}

		if r.Method == "POST" {
			for {
				var err error
				s := "DELETE FROM cat WHERE cat_id = ?"
				_, err = sqlexec(db, s, qcatid)
				if err != nil {
					log.Printf("DB error deleting cat: %s\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				s = "UPDATE entrycat SET cat_id = 1 WHERE cat_id = ?"
				_, err = sqlexec(db, s, qcatid)
				if err != nil {
					log.Printf("DB error resetting entrycat: %s\n", err)
					errmsg = "A problem occured. Please try again."
					break
				}

				http.Redirect(w, r, unescapeUrl(qfrom), http.StatusSeeOther)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html")
		site := querySite(db)
		printPageHead(w, site, false)
		printPageNav(w, login, site, nil)

		fmt.Fprintf(w, "<div class=\"main\">\n")
		fmt.Fprintf(w, "<section class=\"main-content\">\n")
		fmt.Fprintf(w, "<form class=\"simpleform\" action=\"/delcat/?catid=%d&from=%s\" method=\"post\">\n", qcatid, url.QueryEscape(qfrom))
		fmt.Fprintf(w, "<h1 class=\"heading\">Delete Category</h1>")
		if errmsg != "" {
			fmt.Fprintf(w, "<div class=\"control\">\n")
			fmt.Fprintf(w, "<p class=\"error\">%s</p>\n", errmsg)
			fmt.Fprintf(w, "</div>\n")
		}
		fmt.Fprintf(w, "<div class=\"control displayonly\">\n")
		fmt.Fprintf(w, "<label for=\"name\">category name</label>\n")
		fmt.Fprintf(w, "<input id=\"name\" name=\"name\" type=\"text\" size=\"20\" maxlength=\"20\" readonly value=\"%s\">\n", escape(cat.Name))
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "<div class=\"control\">\n")
		fmt.Fprintf(w, "<button class=\"submit\">delete category</button>\n")
		fmt.Fprintf(w, "</div>\n")
		fmt.Fprintf(w, "</form>\n")
		fmt.Fprintf(w, "</section>\n")

		fmt.Fprintf(w, "</div>\n")
		printPageFoot(w)
	}
}

func queryEntry(db *sql.DB, entryid int64) (*Entry, error) {
	s := "SELECT e.entry_id, e.thing, e.title, e.url, e.body, e.createdt FROM entry e WHERE e.entry_id = ?"
	row := db.QueryRow(s, entryid)
	var e Entry
	err := row.Scan(&e.Entryid, &e.Thing, &e.Title, &e.Url, &e.Body, &e.Createdt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func queryEntryTags(db *sql.DB, entryid int64) ([]string, error) {
	s := "SELECT tag FROM entrytag WHERE entry_id = ? ORDER BY tag"
	rows, err := db.Query(s, entryid)
	if err != nil {
		return nil, err
	}
	var tt []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		tt = append(tt, t)
	}
	return tt, nil
}

// createHash(), encrypt() and decrypt() copied from Nic Raboy (thanks!) source article:
// https://www.thepolyglotdeveloper.com/2018/02/encrypt-decrypt-data-golang-application-crypto-packages/
func createHash(key string) string {
	hasher := md5.New()
	hasher.Write([]byte(key))
	return hex.EncodeToString(hasher.Sum(nil))
}

func encrypt(data []byte, passphrase string) []byte {
	block, _ := aes.NewCipher([]byte(createHash(passphrase)))
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(err.Error())
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(cryptorand.Reader, nonce); err != nil {
		panic(err.Error())
	}
	ciphertext := gcm.Seal(nonce, nonce, data, nil)
	return ciphertext
}

func decrypt(data []byte, passphrase string) []byte {
	key := []byte(createHash(passphrase))
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err.Error())
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(err.Error())
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return []byte{}
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		panic(err.Error())
	}
	return plaintext
}

//todo Make this configurable and hidden.
const PASSPHRASE = "newsboard_passphrase"

func encryptString(s string) string {
	bs := encrypt([]byte(s), PASSPHRASE)
	return hex.EncodeToString(bs)
}

func decryptString(tok string) string {
	bstok, err := hex.DecodeString(tok)
	if err != nil {
		return ""
	}
	bs := decrypt(bstok, PASSPHRASE)
	return string(bs)
}
