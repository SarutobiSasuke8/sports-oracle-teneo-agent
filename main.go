// Sports Oracle Teneo Agent
//
// A Teneo Protocol agent service that wraps the Sports Oracle API
// (https://sports-oracle.vercel.app). It receives Teneo agent commands over
// HTTP, proxies them to the Sports Oracle REST API, and returns structured
// JSON responses. It also exposes an /mcp endpoint that transparently
// forwards MCP traffic to the Sports Oracle MCP server.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	defaultPort    = "8080"
	defaultBaseURL = "https://sports-oracle.vercel.app"
	// Sandbox key for testing only. Live access requires a staked key —
	// set SPORTS_ORACLE_KEY to override.
	sandboxKey = "sk_test_886492645fd15c41a37c4101c8b616a2"

	apiPathPrefix = "/api/v1"
	mcpPath       = "/api/mcp"
)

// Sports commands the agent exposes. This must stay in sync with the commands
// in sports-oracle-agent-metadata.json, which is what Teneo prices and
// advertises: serving a sport that is not listed there means serving it unpaid.
var supportedSports = map[string]bool{
	"nba": true, "nhl": true, "mlb": true, "nfl": true,
	"f1": true, "soccer": true, "tennis": true, "mma": true,
}

var supportedResources = map[string]bool{
	"injuries": true, "scores": true, "schedule": true,
	"standings": true, "teams": true,
}

// commandRequest is the payload a Teneo runtime delivers for a command
// invocation. The command names the sport; params carries the resource plus
// any optional filters, which are forwarded upstream as query parameters.
type commandRequest struct {
	Command string            `json:"command"`
	Params  map[string]string `json:"params"`
}

type commandResponse struct {
	Success  bool        `json:"success"`
	Command  string      `json:"command,omitempty"`
	Resource string      `json:"resource,omitempty"`
	Data     interface{} `json:"data,omitempty"`
	Error    *agentError `json:"error,omitempty"`
}

type agentError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type server struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func main() {
	port := envOr("PORT", defaultPort)
	baseURL := strings.TrimRight(envOr("SPORTS_ORACLE_BASE_URL", defaultBaseURL), "/")
	apiKey := envOr("SPORTS_ORACLE_KEY", sandboxKey)

	if apiKey == sandboxKey {
		log.Println("warning: using sandbox API key; set SPORTS_ORACLE_KEY for live data")
	}

	s := &server{
		baseURL: baseURL,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/command", s.handleCommand)
	mux.HandleFunc("/mcp", s.handleMCP)

	addr := ":" + port
	log.Printf("sports-oracle teneo agent listening on %s (upstream %s)", addr, baseURL)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"agent":   "sports-oracle",
		"version": "1.0.0",
	})
}

// handleCommand executes a Teneo agent command: {"command":"nba","params":{"resource":"injuries"}}
func (s *server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_error", "could not read request body")
		return
	}

	var req commandRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be JSON: "+err.Error())
		return
	}

	sport := strings.ToLower(strings.TrimSpace(req.Command))
	if sport == "" {
		writeError(w, http.StatusBadRequest, "missing_command", "command is required (e.g. nba, nfl, soccer)")
		return
	}
	if !supportedSports[sport] {
		writeError(w, http.StatusBadRequest, "unknown_command",
			fmt.Sprintf("unsupported sport %q; supported: %s", sport, keys(supportedSports)))
		return
	}

	resource := strings.ToLower(strings.TrimSpace(req.Params["resource"]))
	if resource == "" {
		writeError(w, http.StatusBadRequest, "missing_resource",
			"params.resource is required; supported: "+keys(supportedResources))
		return
	}
	if !supportedResources[resource] {
		writeError(w, http.StatusBadRequest, "unknown_resource",
			fmt.Sprintf("unsupported resource %q; supported: %s", resource, keys(supportedResources)))
		return
	}

	// Forward remaining params (team, date, league, ...) as query parameters.
	query := url.Values{}
	for k, v := range req.Params {
		if k != "resource" && v != "" {
			query.Set(k, v)
		}
	}

	data, status, err := s.fetchOracle(sport, resource, query)
	if err != nil {
		log.Printf("upstream error for %s/%s: %v", sport, resource, err)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	if status < 200 || status >= 300 {
		writeJSON(w, status, commandResponse{
			Success:  false,
			Command:  sport,
			Resource: resource,
			Error: &agentError{
				Code:    fmt.Sprintf("oracle_http_%d", status),
				Message: upstreamErrorMessage(data, status),
			},
		})
		return
	}

	writeJSON(w, http.StatusOK, commandResponse{
		Success:  true,
		Command:  sport,
		Resource: resource,
		Data:     data,
	})
}

// handleMCP proxies MCP transport traffic (JSON-RPC over HTTP) to the
// Sports Oracle MCP endpoint, injecting the oracle key.
func (s *server) handleMCP(w http.ResponseWriter, r *http.Request) {
	upstreamURL := s.baseURL + mcpPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_error", err.Error())
		return
	}
	for _, h := range []string{"Content-Type", "Accept", "Mcp-Session-Id", "Last-Event-Id"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	req.Header.Set("X-Oracle-Key", s.apiKey)

	resp, err := s.client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "MCP upstream unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	for _, h := range []string{"Content-Type", "Mcp-Session-Id"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("mcp proxy copy error: %v", err)
	}
}

// fetchOracle performs a GET against /api/v1/{sport}/{resource} and decodes
// the JSON body. Non-JSON bodies are returned as a raw string so callers
// still see upstream error text.
func (s *server) fetchOracle(sport, resource string, query url.Values) (interface{}, int, error) {
	u := fmt.Sprintf("%s%s/%s/%s", s.baseURL, apiPathPrefix, sport, resource)
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Oracle-Key", s.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("sports oracle unreachable: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading upstream response: %w", err)
	}

	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		// On an error status, a non-JSON body is still useful as error text;
		// the caller surfaces it via upstreamErrorMessage. On a 2xx it means
		// we never reached the API (an SPA fallback page, a proxy
		// interstitial), so fail loudly rather than bill for a web page.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return string(body), resp.StatusCode, nil
		}
		return nil, resp.StatusCode, fmt.Errorf(
			"expected JSON from sports oracle, got %q (HTTP %d); check that %s serves the API",
			resp.Header.Get("Content-Type"), resp.StatusCode, u)
	}
	return data, resp.StatusCode, nil
}

func upstreamErrorMessage(data interface{}, status int) string {
	if m, ok := data.(map[string]interface{}); ok {
		for _, k := range []string{"error", "message", "detail"} {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
	}
	if s, ok := data.(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return fmt.Sprintf("sports oracle returned HTTP %d", status)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write error: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, commandResponse{
		Success: false,
		Error:   &agentError{Code: code, Message: message},
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func keys(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
