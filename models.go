package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// modelsFetchTimeout bounds each upstream /v1/models call. Claude Code's
	// gateway model discovery gives up after 3 seconds by default.
	modelsFetchTimeout = 2 * time.Second
	modelsCacheTTL     = 5 * time.Minute
	// modelsRetryAfter is how long a failed upstream is not asked again, so a
	// down upstream doesn't slow every /v1/models request by the timeout.
	modelsRetryAfter = 30 * time.Second
	// maxModelPages caps pagination of an Anthropic-format upstream.
	maxModelPages = 10
)

const (
	routeAPIOpenAI    = "openai"
	routeAPIAnthropic = "anthropic"
)

// routeAPI returns the API format of a route's upstream: the configured "api"
// value, or "anthropic" for api.anthropic.com and "openai" otherwise.
func routeAPI(rt Route) string {
	switch strings.ToLower(strings.TrimSpace(rt.API)) {
	case routeAPIAnthropic:
		return routeAPIAnthropic
	case routeAPIOpenAI:
		return routeAPIOpenAI
	}
	if u, err := url.Parse(strings.TrimSpace(rt.BaseURL)); err == nil && strings.EqualFold(u.Hostname(), "api.anthropic.com") {
		return routeAPIAnthropic
	}
	return routeAPIOpenAI
}

type modelObject = map[string]any

type modelsCacheEntry struct {
	models    map[string]modelObject // upstream model objects keyed by id
	fetchedAt time.Time
	failedAt  time.Time
}

type modelsCache struct {
	mu      sync.Mutex
	entries map[string]*modelsCacheEntry
}

func routeCacheKey(rt Route) string {
	return strings.TrimSpace(rt.BaseURL) + "\x00" + rt.APIKeyEnv + "\x00" + routeAPI(rt)
}

// upstreamModels returns the model objects an upstream reports, from cache
// when fresh. It returns nil when the upstream has never answered.
func (g *Gateway) upstreamModels(ctx context.Context, rt Route, inbound *http.Request) map[string]modelObject {
	key := routeCacheKey(rt)
	now := time.Now()

	g.models.mu.Lock()
	e := g.models.entries[key]
	if e != nil && (now.Sub(e.fetchedAt) < modelsCacheTTL || now.Sub(e.failedAt) < modelsRetryAfter) {
		models := e.models
		g.models.mu.Unlock()
		return models
	}
	g.models.mu.Unlock()

	fetched, err := g.fetchUpstreamModels(ctx, rt, inbound)

	g.models.mu.Lock()
	defer g.models.mu.Unlock()
	if g.models.entries == nil {
		g.models.entries = map[string]*modelsCacheEntry{}
	}
	e = g.models.entries[key]
	if e == nil {
		e = &modelsCacheEntry{}
		g.models.entries[key] = e
	}
	if err != nil {
		// Keep serving stale data, if any.
		e.failedAt = now
		return e.models
	}
	e.models, e.fetchedAt, e.failedAt = fetched, now, time.Time{}
	return e.models
}

func (g *Gateway) fetchUpstreamModels(ctx context.Context, rt Route, inbound *http.Request) (map[string]modelObject, error) {
	ctx, cancel := context.WithTimeout(ctx, modelsFetchTimeout)
	defer cancel()

	api := routeAPI(rt)
	base, err := buildUpstreamURL(rt.BaseURL, "/v1/models")
	if err != nil {
		return nil, err
	}

	models := map[string]modelObject{}
	afterID := ""
	for page := 0; page < maxModelPages; page++ {
		q := url.Values{"limit": {"1000"}}
		if afterID != "" {
			q.Set("after_id", afterID)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if api == routeAPIAnthropic {
			applyAnthropicHeaders(req, inbound, rt)
		} else {
			applyAuthorizationHeader(req, inbound, rt)
		}

		resp, err := g.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("upstream %s returned %d", base, resp.StatusCode)
		}

		var list struct {
			Data    []json.RawMessage `json:"data"`
			HasMore bool              `json:"has_more"`
			LastID  string            `json:"last_id"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("upstream %s: invalid models list: %w", base, err)
		}
		for _, raw := range list.Data {
			obj, err := decodeObject(raw)
			if err != nil {
				continue
			}
			if id, ok := obj["id"].(string); ok && id != "" {
				models[id] = obj
			}
		}
		if !list.HasMore || list.LastID == "" || list.LastID == afterID {
			break
		}
		afterID = list.LastID
	}
	return models, nil
}

func decodeObject(raw []byte) (modelObject, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber() // keep integers such as "created" exact
	var obj modelObject
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// normalizeModel returns a copy of the upstream object (or a minimal one when
// the upstream didn't report the model) that OpenAI and Anthropic clients can
// both read: it adds "object" and "type", and fills "created" (Unix seconds)
// and "created_at" (RFC 3339) from each other. Upstream fields are kept as is.
func normalizeModel(id string, upstream modelObject, api string) modelObject {
	m := modelObject{}
	for k, v := range upstream {
		m[k] = v
	}
	m["id"] = id
	if _, ok := m["object"]; !ok {
		m["object"] = "model"
	}
	if _, ok := m["type"]; !ok {
		m["type"] = "model"
	}
	if _, ok := m["created"]; !ok {
		if s, ok := m["created_at"].(string); ok {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				m["created"] = t.Unix()
			}
		}
	}
	if _, ok := m["created_at"]; !ok {
		if n, ok := m["created"].(json.Number); ok {
			if sec, err := n.Int64(); err == nil {
				m["created_at"] = time.Unix(sec, 0).UTC().Format(time.RFC3339)
			}
		}
	}
	if _, ok := m["owned_by"]; !ok && api == routeAPIAnthropic {
		m["owned_by"] = "anthropic"
	}
	return m
}

// configuredModels lists the configured models in config order, without
// duplicates, with the route that serves each.
func (g *Gateway) configuredModels() ([]string, map[string]Route) {
	var ids []string
	routes := map[string]Route{}
	for _, rt := range g.cfg.Routes {
		for _, m := range rt.Models {
			if _, ok := routes[m]; ok {
				continue
			}
			routes[m] = rt
			ids = append(ids, m)
		}
	}
	return ids, routes
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	writeErr := modelsErrWriter(r)
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}

	ids, routes := g.configuredModels()

	// Ask each distinct upstream once, in parallel.
	upstream := map[string]map[string]modelObject{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, rt := range g.cfg.Routes {
		key := routeCacheKey(rt)
		mu.Lock()
		_, started := upstream[key]
		upstream[key] = nil
		mu.Unlock()
		if started {
			continue
		}
		wg.Add(1)
		go func(rt Route, key string) {
			defer wg.Done()
			models := g.upstreamModels(r.Context(), rt, r)
			mu.Lock()
			upstream[key] = models
			mu.Unlock()
		}(rt, key)
	}
	wg.Wait()

	data := make([]modelObject, 0, len(ids))
	for _, id := range ids {
		rt := routes[id]
		data = append(data, normalizeModel(id, upstream[routeCacheKey(rt)][id], routeAPI(rt)))
	}

	resp := map[string]any{
		"object":   "list",
		"data":     data,
		"has_more": false,
		"first_id": nil,
		"last_id":  nil,
	}
	if len(ids) > 0 {
		resp["first_id"] = ids[0]
		resp["last_id"] = ids[len(ids)-1]
	}
	writeJSON(w, http.StatusOK, resp)
}

func (g *Gateway) handleModel(w http.ResponseWriter, r *http.Request) {
	writeErr := modelsErrWriter(r)
	id := r.PathValue("id")
	_, routes := g.configuredModels()
	rt, ok := routes[id]
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found_error", fmt.Sprintf("model not found: %s", id))
		return
	}
	models := g.upstreamModels(r.Context(), rt, r)
	writeJSON(w, http.StatusOK, normalizeModel(id, models[id], routeAPI(rt)))
}

// modelsErrWriter answers Anthropic clients, which send anthropic-version,
// with Anthropic-format errors and everyone else with OpenAI-format errors.
func modelsErrWriter(r *http.Request) errWriterFunc {
	if r.Header.Get("anthropic-version") != "" {
		return writeAnthropicErr
	}
	return writeErr
}
