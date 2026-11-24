package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTestGateway(routes ...Route) (*Gateway, *http.ServeMux) {
	g := &Gateway{cfg: Config{Routes: routes}, client: &http.Client{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", g.handleModels)
	mux.HandleFunc("GET /v1/models/{id...}", g.handleModel)
	return g, mux
}

func getJSON(t *testing.T, mux *http.ServeMux, path string, header map[string]string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: invalid JSON %q: %v", path, rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestModelsMergesUpstreamInfo(t *testing.T) {
	// OpenAI-compatible upstream (vLLM style).
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer local-key" {
			t.Errorf("openai upstream Authorization = %q", got)
		}
		w.Write([]byte(`{"object":"list","data":[
			{"id":"openai/gpt-oss-20b","object":"model","created":1760000000,"owned_by":"vllm","root":"openai/gpt-oss-20b","max_model_len":131072},
			{"id":"unlisted","object":"model","created":1,"owned_by":"vllm"}]}`))
	}))
	defer openai.Close()

	// Anthropic-format upstream with two pages.
	var anthropicCalls atomic.Int32
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicCalls.Add(1)
		if r.Header.Get("x-api-key") != "sk-test" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("anthropic upstream headers: x-api-key=%q version=%q", r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"))
		}
		if r.URL.Query().Get("after_id") == "" {
			w.Write([]byte(`{"data":[{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5","created_at":"2026-10-01T00:00:00Z","max_input_tokens":1000000,"max_tokens":128000}],"has_more":true,"first_id":"claude-opus-5-5","last_id":"claude-opus-5-5"}`))
			return
		}
		w.Write([]byte(`{"data":[{"type":"model","id":"claude-haiku-5-5","display_name":"Claude Haiku 5.5","created_at":"2026-09-01T00:00:00Z"}],"has_more":false,"first_id":"claude-haiku-5-5","last_id":"claude-haiku-5-5"}`))
	}))
	defer anthropic.Close()

	t.Setenv("TEST_LOCAL_KEY", "local-key")
	t.Setenv("TEST_ANTHROPIC_KEY", "sk-test")
	_, mux := newTestGateway(
		Route{BaseURL: anthropic.URL, APIKeyEnv: "TEST_ANTHROPIC_KEY", API: "anthropic", Models: []string{"claude-opus-5-5", "claude-haiku-5-5", "claude-missing"}},
		Route{BaseURL: openai.URL, APIKeyEnv: "TEST_LOCAL_KEY", Models: []string{"openai/gpt-oss-20b"}},
	)

	code, body := getJSON(t, mux, "/v1/models?limit=1000", nil)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if body["object"] != "list" || body["has_more"] != false || body["first_id"] != "claude-opus-5-5" || body["last_id"] != "openai/gpt-oss-20b" {
		t.Errorf("envelope = %v", body)
	}
	data := body["data"].([]any)
	if len(data) != 4 {
		t.Fatalf("got %d models, want 4 (unlisted upstream models must be hidden): %v", len(data), data)
	}

	opus := data[0].(map[string]any)
	for k, want := range map[string]any{
		"id": "claude-opus-5-5", "type": "model", "object": "model", "display_name": "Claude Opus 5.5",
		"created_at": "2026-10-01T00:00:00Z", "created": float64(1790812800), "max_input_tokens": float64(1000000), "owned_by": "anthropic",
	} {
		if opus[k] != want {
			t.Errorf("opus[%s] = %v, want %v", k, opus[k], want)
		}
	}
	if haiku := data[1].(map[string]any); haiku["display_name"] != "Claude Haiku 5.5" {
		t.Errorf("second page not merged: %v", haiku)
	}
	if missing := data[2].(map[string]any); len(missing) != 4 || missing["id"] != "claude-missing" || missing["owned_by"] != "anthropic" {
		t.Errorf("model unknown upstream should be minimal: %v", missing)
	}
	gpt := data[3].(map[string]any)
	for k, want := range map[string]any{
		"id": "openai/gpt-oss-20b", "object": "model", "type": "model", "owned_by": "vllm",
		"created": float64(1760000000), "created_at": "2025-10-09T08:53:20Z", "max_model_len": float64(131072),
	} {
		if gpt[k] != want {
			t.Errorf("gpt[%s] = %v, want %v", k, gpt[k], want)
		}
	}

	// Retrieve by id (ids may contain "/"), served from cache.
	before := anthropicCalls.Load()
	code, m := getJSON(t, mux, "/v1/models/openai/gpt-oss-20b", nil)
	if code != http.StatusOK || m["max_model_len"] != float64(131072) {
		t.Errorf("retrieve = %d %v", code, m)
	}
	code, m = getJSON(t, mux, "/v1/models/claude-opus-5-5", nil)
	if code != http.StatusOK || m["display_name"] != "Claude Opus 5.5" {
		t.Errorf("retrieve = %d %v", code, m)
	}
	if anthropicCalls.Load() != before {
		t.Errorf("upstream called again within cache TTL")
	}

	code, m = getJSON(t, mux, "/v1/models/nope", map[string]string{"anthropic-version": "2023-06-01"})
	if code != http.StatusNotFound || m["type"] != "error" {
		t.Errorf("unknown model = %d %v", code, m)
	}
}

func TestModelsUpstreamDown(t *testing.T) {
	var calls atomic.Int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer down.Close()

	_, mux := newTestGateway(Route{BaseURL: down.URL, Models: []string{"m1"}})
	for i := 0; i < 2; i++ {
		code, body := getJSON(t, mux, "/v1/models", nil)
		data := body["data"].([]any)
		if code != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["id"] != "m1" {
			t.Fatalf("fallback = %d %v", code, body)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("failed upstream asked %d times, want 1 within retry window", calls.Load())
	}
}

func TestModelsUpstreamSlow(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()

	_, mux := newTestGateway(Route{BaseURL: slow.URL, Models: []string{"m1"}})
	start := time.Now()
	code, _ := getJSON(t, mux, "/v1/models", nil)
	if code != http.StatusOK || time.Since(start) > modelsFetchTimeout+time.Second {
		t.Errorf("status %d after %v", code, time.Since(start))
	}
}

func TestRouteAPI(t *testing.T) {
	for _, tc := range []struct {
		rt   Route
		want string
	}{
		{Route{BaseURL: "https://api.anthropic.com"}, "anthropic"},
		{Route{BaseURL: "https://api.openai.com"}, "openai"},
		{Route{BaseURL: "http://192.168.0.1:8000", API: "Anthropic"}, "anthropic"},
		{Route{BaseURL: "https://api.anthropic.com", API: "openai"}, "openai"},
	} {
		if got := routeAPI(tc.rt); got != tc.want {
			t.Errorf("routeAPI(%+v) = %s, want %s", tc.rt, got, tc.want)
		}
	}
}
