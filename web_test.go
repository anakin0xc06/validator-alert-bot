package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireBasicAuth_RejectsMissingOrWrongCredentials(t *testing.T) {
	called := false
	handler := requireBasicAuth("admin", "secret", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name           string
		setAuth        bool
		user, password string
	}{
		{"no credentials", false, "", ""},
		{"wrong username", true, "nope", "secret"},
		{"wrong password", true, "admin", "nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.setAuth {
				req.SetBasicAuth(c.user, c.password)
			}
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatalf("expected WWW-Authenticate header on 401")
			}
			if called {
				t.Fatalf("wrapped handler must not run when auth fails")
			}
		})
	}
}

func TestRequireBasicAuth_AllowsCorrectCredentials(t *testing.T) {
	called := false
	handler := requireBasicAuth("admin", "secret", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !called {
		t.Fatalf("expected wrapped handler to run with correct credentials")
	}
}

func TestGroupDashboardRowsByNetwork(t *testing.T) {
	rows := []dashboardRow{
		{Network: "cosmos", Moniker: "a"},
		{Network: "cosmos", Moniker: "b"},
		{Network: "akash", Moniker: "c"},
	}
	groups := groupDashboardRowsByNetwork(rows)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d: %+v", len(groups), groups)
	}
	if groups[0].Name != "cosmos" || len(groups[0].Rows) != 2 {
		t.Fatalf("expected cosmos group with 2 rows, got %+v", groups[0])
	}
	if groups[1].Name != "akash" || len(groups[1].Rows) != 1 {
		t.Fatalf("expected akash group with 1 row, got %+v", groups[1])
	}

	if got := groupDashboardRowsByNetwork(nil); len(got) != 0 {
		t.Fatalf("expected no groups for empty input, got %+v", got)
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
