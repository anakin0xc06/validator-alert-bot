package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anakin0xc06/validator-alert-bot/config"
)

// withWebCredentials sets config.WebUsername/WebPassword for the duration
// of the test, restoring the previous values afterward
func withWebCredentials(t *testing.T, user, pass string) {
	t.Helper()
	prevUser, prevPass := config.WebUsername, config.WebPassword
	config.WebUsername, config.WebPassword = user, pass
	t.Cleanup(func() {
		config.WebUsername, config.WebPassword = prevUser, prevPass
	})
}

func TestLoginHandler_GET_ShowsForm(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	loginHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `action="/login"`) {
		t.Fatalf("expected a login form in the response, got:\n%s", rec.Body.String())
	}
}

func TestLoginHandler_GET_WithValidSessionRedirectsToDashboard(t *testing.T) {
	token, err := createSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteSession(token) })

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	loginHandler(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("expected redirect to /, got status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLoginHandler_POST_WrongCredentialsRejected(t *testing.T) {
	withWebCredentials(t, "admin", "secret")

	form := url.Values{"username": {"admin"}, "password": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	loginHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Invalid username or password") {
		t.Fatalf("expected an error message, got:\n%s", rec.Body.String())
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("did not expect a session cookie on failed login")
	}
}

func TestLoginHandler_POST_CorrectCredentialsSetsSessionAndRedirects(t *testing.T) {
	withWebCredentials(t, "admin", "secret")

	form := url.Values{"username": {"admin"}, "password": {"secret"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	loginHandler(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("expected redirect to /, got status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	resp := rec.Result()
	t.Cleanup(func() {
		for _, c := range resp.Cookies() {
			if c.Name == sessionCookieName {
				deleteSession(c.Value)
			}
		}
	})
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			found = true
			if !validSession(&http.Request{Header: http.Header{"Cookie": []string{c.Name + "=" + c.Value}}}) {
				t.Fatalf("expected the issued session to be valid")
			}
		}
	}
	if !found {
		t.Fatalf("expected a session cookie to be set")
	}
}

func TestRequireSession_RedirectsWithoutValidSession(t *testing.T) {
	called := false
	handler := requireSession(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("expected redirect to /login, got status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if called {
		t.Fatalf("wrapped handler must not run without a valid session")
	}
}

func TestRequireSession_AllowsValidSession(t *testing.T) {
	token, err := createSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteSession(token) })

	called := false
	handler := requireSession(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK || !called {
		t.Fatalf("expected the wrapped handler to run, got status=%d called=%v", rec.Code, called)
	}
}

func TestValidSession_ExpiredSessionIsRejectedAndEvicted(t *testing.T) {
	const token = "expired-test-token"
	sessionsMu.Lock()
	sessions[token] = time.Now().Add(-time.Minute)
	sessionsMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	if validSession(req) {
		t.Fatalf("expected an expired session to be invalid")
	}
	sessionsMu.Lock()
	_, stillPresent := sessions[token]
	sessionsMu.Unlock()
	if stillPresent {
		t.Fatalf("expected the expired session to be evicted from the store")
	}
}

func TestLogoutHandler_ClearsSessionAndRedirects(t *testing.T) {
	token, err := createSession()
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	logoutHandler(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("expected redirect to /login, got status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	sessionsMu.Lock()
	_, stillPresent := sessions[token]
	sessionsMu.Unlock()
	if stillPresent {
		t.Fatalf("expected the session to be removed from the store after logout")
	}
}

// newFixtureRESTServer serves canned slashing-module JSON so
// collectDashboardRows can be exercised without hitting a real chain
func newFixtureRESTServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cosmos/slashing/v1beta1/signing_infos/testvalcons1abcdefgh", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"val_signing_info":{"address":"testvalcons1abcdefgh","start_height":"0","index_offset":"0","jailed_until":"1970-01-01T00:00:00Z","tombstoned":false,"missed_blocks_counter":"500"}}`))
	})
	mux.HandleFunc("/cosmos/slashing/v1beta1/params", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"params":{"signed_blocks_window":"10000","min_signed_per_window":"0.050000000000000000"}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// withFixtureValidator registers a throwaway network/alias pair pointing at
// server for the duration of the test, cleaning up afterward so no state
// leaks into other tests sharing the package-level maps.
func withFixtureValidator(t *testing.T, server *httptest.Server) string {
	t.Helper()
	const address = "testvalcons1abcdefgh"
	const prefix = "test"

	networks[prefix] = map[string]string{"rest": server.URL}
	validatorAliases[address] = ValidatorAlias{
		Network:          "testnet",
		Moniker:          "Test Validator",
		ValconsAddress:   address,
		ValidatorAddress: "testvaloper1abcdefgh",
	}
	t.Cleanup(func() {
		delete(networks, prefix)
		delete(validatorAliases, address)
		stateMu.Lock()
		delete(slashingParamsCache, prefix)
		stateMu.Unlock()
	})
	return address
}

func TestCollectDashboardRows_FetchesAndComputesUptime(t *testing.T) {
	server := newFixtureRESTServer(t)
	withFixtureValidator(t, server)

	rows := collectDashboardRows()
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.Note != "" {
		t.Fatalf("expected no note, got %q", row.Note)
	}
	if row.Moniker != "Test Validator" || row.Network != "testnet" {
		t.Fatalf("unexpected identity fields: %+v", row)
	}
	if row.Missed != 500 || row.Window != 10000 {
		t.Fatalf("expected missed=500 window=10000, got missed=%d window=%d", row.Missed, row.Window)
	}
	if row.UptimePct != 95 {
		t.Fatalf("expected uptime=95, got %.2f", row.UptimePct)
	}
	if !row.Safe {
		t.Fatalf("expected safe (95%% signed against a 5%% min_signed_per_window bar)")
	}
	if row.Jailed {
		t.Fatalf("did not expect jailed")
	}
}

func TestDashboardHandler_RendersHTMLWithRows(t *testing.T) {
	server := newFixtureRESTServer(t)
	withFixtureValidator(t, server)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	dashboardHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Test Validator", "testnet", "95.00%", "SAFE"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected response to contain %q, got:\n%s", want, body)
		}
	}
}
