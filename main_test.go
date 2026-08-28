package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mustReadMetadata loads the single Teneo metadata file in the repo root. The
// CLI also requires that exactly one such file exists.
func mustReadMetadata(t *testing.T) []byte {
	t.Helper()
	// The CLI discovers metadata by the "-metadata.json" suffix and requires
	// exactly one match in the agent directory.
	matches, err := filepath.Glob("*-metadata.json")
	if err != nil {
		t.Fatalf("globbing for metadata: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d metadata files (%v), want exactly 1", len(matches), matches)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("reading %s: %v", matches[0], err)
	}
	return raw
}

// newTestServer builds an agent pointed at a stub upstream.
func newTestServer(t *testing.T, upstream http.HandlerFunc) (*server, func()) {
	t.Helper()
	stub := httptest.NewServer(upstream)
	s := &server{
		baseURL: stub.URL,
		apiKey:  "test-key",
		client:  &http.Client{Timeout: 5 * time.Second},
	}
	return s, stub.Close
}

func postCommand(t *testing.T, s *server, body string) (int, commandResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/command", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCommand(rec, req)

	var out commandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response was not valid JSON: %v (body: %s)", err, rec.Body.String())
	}
	return rec.Code, out
}

// A 200 carrying HTML means we hit an SPA fallback rather than the API. The
// agent must not report success, because a success is a billable answer.
func TestNonJSONSuccessIsAnError(t *testing.T) {
	s, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<!doctype html><html><body>Sports Oracle</body></html>"))
	})
	defer done()

	status, resp := postCommand(t, s, `{"command":"nba","params":{"resource":"injuries"}}`)

	if resp.Success {
		t.Fatalf("HTML upstream body reported as success; data was %v", resp.Data)
	}
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", status, http.StatusBadGateway)
	}
	if resp.Error == nil || resp.Error.Code != "upstream_error" {
		t.Errorf("error = %+v, want code upstream_error", resp.Error)
	}
}

func TestValidJSONSucceeds(t *testing.T) {
	s, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Oracle-Key"); got != "test-key" {
			t.Errorf("X-Oracle-Key = %q, want test-key", got)
		}
		if got := r.URL.Path; got != "/api/v1/nba/injuries" {
			t.Errorf("path = %q, want /api/v1/nba/injuries", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"injuries":[{"player":"Doe","status":"out"}]}`))
	})
	defer done()

	status, resp := postCommand(t, s, `{"command":"nba","params":{"resource":"injuries"}}`)

	if !resp.Success {
		t.Fatalf("valid JSON not reported as success: %+v", resp.Error)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if resp.Command != "nba" || resp.Resource != "injuries" {
		t.Errorf("echoed command/resource = %q/%q, want nba/injuries", resp.Command, resp.Resource)
	}
}

// Non-JSON on an error status is still the clearest description of what went
// wrong, so it should reach the caller rather than being replaced.
func TestUpstreamErrorTextIsPreserved(t *testing.T) {
	s, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid oracle key"))
	})
	defer done()

	status, resp := postCommand(t, s, `{"command":"nba","params":{"resource":"injuries"}}`)

	if resp.Success {
		t.Fatal("401 upstream reported as success")
	}
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "invalid oracle key") {
		t.Errorf("error = %+v, want upstream text preserved", resp.Error)
	}
}

// Params other than resource become upstream query parameters.
func TestExtraParamsForwardedAsQuery(t *testing.T) {
	var gotQuery string
	s, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("team")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
	defer done()

	if _, resp := postCommand(t, s, `{"command":"nba","params":{"resource":"scores","team":"LAL"}}`); !resp.Success {
		t.Fatalf("request failed: %+v", resp.Error)
	}
	if gotQuery != "LAL" {
		t.Errorf("team query = %q, want LAL", gotQuery)
	}
}

// Sports the metadata does not price must be rejected, not served for free.
func TestUnpricedSportsRejected(t *testing.T) {
	s, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream called for a sport that is not in the metadata")
	})
	defer done()

	for _, sport := range []string{"wnba", "esports", "cricket"} {
		status, resp := postCommand(t, s, `{"command":"`+sport+`","params":{"resource":"scores"}}`)
		if resp.Success {
			t.Errorf("%s: served despite not being priced in metadata", sport)
		}
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", sport, status)
		}
	}
}

func TestValidationRejectsBadRequests(t *testing.T) {
	s, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream called for an invalid request")
	})
	defer done()

	cases := []struct{ name, body, wantCode string }{
		{"missing command", `{"params":{"resource":"scores"}}`, "missing_command"},
		{"unknown command", `{"command":"chess","params":{"resource":"scores"}}`, "unknown_command"},
		{"missing resource", `{"command":"nba","params":{}}`, "missing_resource"},
		{"unknown resource", `{"command":"nba","params":{"resource":"foo"}}`, "unknown_resource"},
		{"malformed json", `{"command":`, "invalid_json"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, resp := postCommand(t, s, tc.body)
			if resp.Success {
				t.Fatal("invalid request reported as success")
			}
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", status)
			}
			if resp.Error == nil || resp.Error.Code != tc.wantCode {
				t.Errorf("error = %+v, want code %s", resp.Error, tc.wantCode)
			}
		})
	}
}

// Commands are case and whitespace insensitive.
func TestCommandNormalization(t *testing.T) {
	s, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nba/injuries" {
			t.Errorf("path = %q, want /api/v1/nba/injuries", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
	defer done()

	if _, resp := postCommand(t, s, `{"command":"  NBA  ","params":{"resource":"INJURIES"}}`); !resp.Success {
		t.Fatalf("normalized command rejected: %+v", resp.Error)
	}
}

// The API key must come from the environment; there is no bundled fallback.
func TestAPIKeyRequiredFromEnv(t *testing.T) {
	t.Setenv("SPORTS_ORACLE_KEY", "")
	if _, err := requiredEnv("SPORTS_ORACLE_KEY"); err == nil {
		t.Fatal("missing SPORTS_ORACLE_KEY did not produce an error")
	}

	t.Setenv("SPORTS_ORACLE_KEY", "sk_test_from_env")
	got, err := requiredEnv("SPORTS_ORACLE_KEY")
	if err != nil {
		t.Fatalf("set SPORTS_ORACLE_KEY produced error: %v", err)
	}
	if got != "sk_test_from_env" {
		t.Errorf("requiredEnv = %q, want sk_test_from_env", got)
	}
}

func getHealth(t *testing.T, s *server) map[string]string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rec.Code)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("health response was not valid JSON: %v (body: %s)", err, rec.Body.String())
	}
	return out
}

// A JSON-speaking upstream is reported as reachable.
func TestHealthReportsUpstreamOK(t *testing.T) {
	s, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"teams":[]}`))
	})
	defer done()

	out := getHealth(t, s)
	if out["status"] != "ok" {
		t.Errorf("status = %q, want ok", out["status"])
	}
	if out["upstream"] != "ok" {
		t.Errorf("upstream = %q, want ok", out["upstream"])
	}
}

// An SPA HTML shell is not the API: health must say so, without failing the
// health response itself.
func TestHealthReportsUpstreamUnreachableOnHTML(t *testing.T) {
	s, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<!doctype html><html><body>Sports Oracle</body></html>"))
	})
	defer done()

	out := getHealth(t, s)
	if out["status"] != "ok" {
		t.Errorf("status = %q, want ok even when upstream is down", out["status"])
	}
	if out["upstream"] != "unreachable" {
		t.Errorf("upstream = %q, want unreachable", out["upstream"])
	}
}

// A dead upstream (connection refused) is reported as unreachable.
func TestHealthReportsUpstreamUnreachableOnDeadServer(t *testing.T) {
	s, done := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	done() // close the stub so the probe gets connection refused

	out := getHealth(t, s)
	if out["status"] != "ok" {
		t.Errorf("status = %q, want ok even when upstream is down", out["status"])
	}
	if out["upstream"] != "unreachable" {
		t.Errorf("upstream = %q, want unreachable", out["upstream"])
	}
}

// Every command in the metadata file must be served by the code, and vice
// versa. This is the mismatch that shipped in the first cut.
func TestMetadataCommandsMatchCode(t *testing.T) {
	var meta struct {
		Commands []struct {
			Name         string   `json:"name"`
			Trigger      string   `json:"trigger"`
			Description  string   `json:"description"`
			PricePerUnit *float64 `json:"pricePerUnit"`
			PriceType    string   `json:"priceType"`
			TaskUnit     string   `json:"taskUnit"`
		} `json:"commands"`
	}
	raw := mustReadMetadata(t)
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("metadata is not valid JSON: %v", err)
	}

	inMeta := map[string]bool{}
	for _, c := range meta.Commands {
		// The Teneo CLI validator reads pricePerUnit/priceType/taskUnit and
		// ignores anything else, so a price in any other field publishes the
		// command as free.
		switch {
		case c.PricePerUnit == nil:
			t.Errorf("command %q has no pricePerUnit; it would publish as free", c.Name)
		case *c.PricePerUnit <= 0:
			t.Errorf("command %q has pricePerUnit %v; it would publish as free", c.Name, *c.PricePerUnit)
		}
		if c.PriceType != "task-transaction" {
			t.Errorf("command %q priceType = %q, want task-transaction", c.Name, c.PriceType)
		}
		if c.TaskUnit != "per-query" && c.TaskUnit != "per-item" {
			t.Errorf("command %q taskUnit = %q, want per-query or per-item", c.Name, c.TaskUnit)
		}
		if c.Trigger == "" || c.Description == "" {
			t.Errorf("command %q needs both trigger and description", c.Name)
		}
		inMeta[c.Name] = true
		if !supportedSports[c.Name] {
			t.Errorf("metadata advertises %q but the code does not serve it", c.Name)
		}
	}
	for sport := range supportedSports {
		if !inMeta[sport] {
			t.Errorf("code serves %q but the metadata does not price it", sport)
		}
	}
}
