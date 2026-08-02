package main

import (
	"crypto/subtle"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/anakin0xc06/validator-alert-bot/config"
)

// dashboardNetworkGroup is dashboardRow entries for one network, for the
// web template to range over
type dashboardNetworkGroup struct {
	Name string
	Rows []dashboardRow
}

// groupDashboardRowsByNetwork groups already (network, moniker)-sorted rows
// into consecutive per-network runs
func groupDashboardRowsByNetwork(rows []dashboardRow) []dashboardNetworkGroup {
	var groups []dashboardNetworkGroup
	for _, row := range rows {
		if len(groups) == 0 || groups[len(groups)-1].Name != row.Network {
			groups = append(groups, dashboardNetworkGroup{Name: row.Network})
		}
		groups[len(groups)-1].Rows = append(groups[len(groups)-1].Rows, row)
	}
	return groups
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
  h1 { font-size: 1.4rem; margin-bottom: .25rem; }
  .updated { color:#8b949e; font-size: .85rem; margin-bottom: 1.5rem; }
  .network { margin-top: 1.5rem; font-size: 1.1rem; font-weight: 600; border-bottom: 1px solid #30363d; padding-bottom: .25rem; }
  table { width: 100%; border-collapse: collapse; margin-top: .5rem; }
  td, th { text-align: left; padding: .4rem .6rem; border-bottom: 1px solid #21262d; font-size: .9rem; }
  th { color: #8b949e; font-weight: 500; }
  .safe { color: #3fb950; font-weight: 600; }
  .unsafe { color: #f85149; font-weight: 600; }
  .note { color: #8b949e; font-style: italic; }
  a { color: #58a6ff; text-decoration: none; }
  a:hover { text-decoration: underline; }
  .empty { color: #8b949e; margin-top: 1rem; }
</style>
</head>
<body>
<h1>📊 Validator Dashboard</h1>
<div class="updated">Last updated {{.Updated}} — refreshes every 60s</div>
{{if not .Networks}}
<div class="empty">No validators configured in validator_aliases.json.</div>
{{end}}
{{range .Networks}}
<div class="network">{{.Name}}</div>
<table>
<tr><th>Validator</th><th>Missed</th><th>Window</th><th>Uptime</th><th>Status</th></tr>
{{range .Rows}}
<tr>
  <td>{{.Moniker}}{{if .MintscanLink}} · <a href="{{.MintscanLink}}" target="_blank" rel="noopener">explorer</a>{{end}}</td>
  {{if .Note}}
  <td colspan="4" class="note">{{.Note}}</td>
  {{else}}
  <td>{{.Missed}}</td>
  <td>{{.Window}}</td>
  <td>{{printf "%.2f" .UptimePct}}%</td>
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
		Updated  string
		Networks []dashboardNetworkGroup
	}{
		Updated:  time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		Networks: groupDashboardRowsByNetwork(collectDashboardRows()),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardPageTemplate.Execute(w, data); err != nil {
		log.Printf("Failed to render dashboard page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// requireBasicAuth gates next behind HTTP Basic Auth, comparing credentials
// in constant time to avoid leaking their length/prefix through timing
func requireBasicAuth(username, password string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="validator dashboard"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// StartWebServer serves the HTML validator dashboard. It does nothing if
// WEB_USERNAME/WEB_PASSWORD aren't both set, so the page is never exposed
// without auth by accident.
func StartWebServer() {
	if config.WebUsername == "" || config.WebPassword == "" {
		log.Println("WEB_USERNAME/WEB_PASSWORD not set, web dashboard disabled")
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", requireBasicAuth(config.WebUsername, config.WebPassword, dashboardHandler))
	log.Printf("Starting web dashboard on %s", config.WebListenAddr)
	if err := http.ListenAndServe(config.WebListenAddr, mux); err != nil {
		log.Printf("Web dashboard server stopped: %v", err)
	}
}
