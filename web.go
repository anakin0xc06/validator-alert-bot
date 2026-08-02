package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"html/template"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/anakin0xc06/validator-alert-bot/config"
)

const (
	sessionCookieName = "vab_session"
	sessionDuration   = 24 * time.Hour
)

var (
	sessionsMu sync.Mutex
	sessions   = make(map[string]time.Time) // token -> expiry
)

// newSessionToken returns a random, 256-bit hex-encoded token, unguessable
// enough that brute-forcing it is infeasible
func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func createSession() (string, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", err
	}
	sessionsMu.Lock()
	sessions[token] = time.Now().Add(sessionDuration)
	sessionsMu.Unlock()
	return token, nil
}

func deleteSession(token string) {
	sessionsMu.Lock()
	delete(sessions, token)
	sessionsMu.Unlock()
}

// validSession reports whether the request carries a live, unexpired
// session cookie, evicting it from the store if it has expired
func validSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	sessionsMu.Lock()
	expires, ok := sessions[cookie.Value]
	sessionsMu.Unlock()
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		deleteSession(cookie.Value)
		return false
	}
	return true
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionDuration),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		deleteSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// requireSession redirects to the login page instead of next when the
// request has no live session, so browsing to the dashboard unauthenticated
// lands on a normal login form rather than a browser auth popup or a bare
// 401 page.
func requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validSession(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

var loginPageTemplate = template.Must(template.New("login").Parse(loginPageHTML))

const loginPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Log in — Validator Dashboard</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 0; background:#0b0f14; color:#e6edf3; display:flex; align-items:center; justify-content:center; min-height:100vh; }
  .card { width: 100%; max-width: 320px; padding: 2rem; background:#111820; border:1px solid #21262d; border-radius: 10px; }
  h1 { font-size: 1.2rem; margin: 0 0 1.25rem; text-align:center; }
  label { display:block; font-size: .85rem; color:#8b949e; margin-bottom: .3rem; }
  input { width:100%; box-sizing:border-box; padding:.55rem .6rem; margin-bottom:1rem; background:#0b0f14; border:1px solid #30363d; border-radius:6px; color:#e6edf3; font-size:.95rem; }
  input:focus { outline: 1px solid #58a6ff; }
  button { width:100%; padding:.6rem; background:#238636; border:none; border-radius:6px; color:#fff; font-size:.95rem; cursor:pointer; }
  button:hover { background:#2ea043; }
  .error { background:#3a1618; border:1px solid #f85149; color:#f85149; padding:.5rem .7rem; border-radius:6px; font-size:.85rem; margin-bottom:1rem; }
</style>
</head>
<body>
<div class="card">
  <h1>📊 Validator Dashboard</h1>
  {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
  <form method="POST" action="/login">
    <label for="username">Username</label>
    <input id="username" type="text" name="username" autofocus required>
    <label for="password">Password</label>
    <input id="password" type="password" name="password" required>
    <button type="submit">Log in</button>
  </form>
</div>
</body>
</html>`

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		user := r.FormValue("username")
		pass := r.FormValue("password")
		if subtle.ConstantTimeCompare([]byte(user), []byte(config.WebUsername)) == 1 &&
			subtle.ConstantTimeCompare([]byte(pass), []byte(config.WebPassword)) == 1 {
			token, err := createSession()
			if err != nil {
				log.Printf("Failed to create session: %v", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			setSessionCookie(w, token)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		loginPageTemplate.Execute(w, struct{ Error string }{"Invalid username or password"})
		return
	}

	if validSession(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	loginPageTemplate.Execute(w, struct{ Error string }{})
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

var dashboardPageTemplate = template.Must(template.New("dashboard").Parse(dashboardPageHTML))

const dashboardPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="60">
<title>Validator Dashboard</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 2rem; background:#0b0f14; color:#e6edf3; }
  .topbar { display:flex; align-items:baseline; justify-content:space-between; flex-wrap:wrap; gap:.5rem; }
  h1 { font-size: 1.4rem; margin: 0 0 .25rem; }
  .updated { color:#8b949e; font-size: .85rem; }
  .logout { color:#8b949e; font-size:.85rem; }
  table { width: 100%; border-collapse: collapse; margin-top: 1rem; }
  td, th { text-align: left; padding: .5rem .6rem; border-bottom: 1px solid #21262d; font-size: .9rem; vertical-align: middle; }
  th { color: #8b949e; font-weight: 500; }
  .safe { color: #3fb950; font-weight: 600; }
  .unsafe { color: #f85149; font-weight: 600; }
  .note { color: #8b949e; font-style: italic; }
  a { color: #58a6ff; text-decoration: none; }
  a:hover { text-decoration: underline; }
  .empty { color: #8b949e; margin-top: 1rem; }
  .uptime-cell { white-space: nowrap; }
  .bar-track { display:inline-block; vertical-align:middle; width:140px; height:8px; background:#21262d; border-radius:4px; overflow:hidden; }
  .bar-fill { height:100%; }
  .bar-fill.safe { background:#3fb950; }
  .bar-fill.unsafe { background:#f85149; }
  .uptime-text { display:inline-block; vertical-align:middle; margin-left:.6rem; font-weight:600; }
  .missed-text { display:block; color:#8b949e; font-size:.75rem; margin-top:.2rem; }
</style>
</head>
<body>
<div class="topbar">
  <h1>📊 Validator Dashboard</h1>
  <a class="logout" href="/logout">Log out</a>
</div>
<div class="updated">Last updated {{.Updated}} — refreshes every 60s</div>
{{if not .Rows}}
<div class="empty">No validators configured in validator_aliases.json.</div>
{{else}}
<table>
<tr><th>Validator</th><th>Network</th><th>Uptime</th><th>Status</th></tr>
{{range .Rows}}
<tr>
  <td>{{.Moniker}}{{if .MintscanLink}} · <a href="{{.MintscanLink}}" target="_blank" rel="noopener">explorer</a>{{end}}</td>
  <td>{{.Network}}</td>
  {{if .Note}}
  <td colspan="2" class="note">{{.Note}}</td>
  {{else}}
  <td class="uptime-cell">
    <div class="bar-track"><div class="bar-fill {{if .Safe}}safe{{else}}unsafe{{end}}" style="width:{{printf "%.1f" .UptimePct}}%"></div></div>
    <span class="uptime-text">{{printf "%.2f" .UptimePct}}%</span>
    <span class="missed-text">{{.Missed}}/{{.Window}} missed</span>
  </td>
  <td class="{{if .Safe}}safe{{else}}unsafe{{end}}">{{if .Safe}}SAFE{{else}}UNSAFE{{end}}{{if .Jailed}} (jailed){{end}}</td>
  {{end}}
</tr>
{{end}}
</table>
{{end}}
</body>
</html>`

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Updated string
		Rows    []dashboardRow
	}{
		Updated: time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		Rows:    collectDashboardRows(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardPageTemplate.Execute(w, data); err != nil {
		log.Printf("Failed to render dashboard page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// StartWebServer serves the HTML validator dashboard behind a login page.
// It does nothing if WEB_USERNAME/WEB_PASSWORD aren't both set, so the page
// is never exposed without auth by accident.
func StartWebServer() {
	if config.WebUsername == "" || config.WebPassword == "" {
		log.Println("WEB_USERNAME/WEB_PASSWORD not set, web dashboard disabled")
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", loginHandler)
	mux.HandleFunc("/logout", logoutHandler)
	mux.HandleFunc("/", requireSession(dashboardHandler))
	log.Printf("Starting web dashboard on %s", config.WebListenAddr)
	if err := http.ListenAndServe(config.WebListenAddr, mux); err != nil {
		log.Printf("Web dashboard server stopped: %v", err)
	}
}
