// Package esi is a small client for the EVE Online ESI endpoints.
//
// Access tokens are supplied per call because each character has its
// own; they are sent only in the Authorization header and never appear in
// errors or logs.
package esi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBaseURL is the production ESI host.
	DefaultBaseURL = "https://esi.evetech.net"

	compatibilityDate = "2020-01-01"
	defaultUserAgent  = "eve-markets"
	maxBodyBytes      = 8 << 20 // largest accepted successful response
	maxErrorBodyBytes = 4 << 10 // largest error body that is read
	maxNameBatch      = 1000    // ids ESI accepts in one /universe/names call
	maxCacheEntries   = 512
)

// Options configures a Client. Zero values select sensible defaults.
type Options struct {
	BaseURL    string
	HTTPClient *http.Client
	UserAgent  string
}

// Client talks to ESI. It is safe for concurrent use.
type Client struct {
	baseURL string
	http    *http.Client
	ua      string

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	etag   string
	body   []byte
	header http.Header
}

// New returns a Client for the given options.
func New(opts Options) *Client {
	c := &Client{
		baseURL: strings.TrimRight(opts.BaseURL, "/"),
		http:    opts.HTTPClient,
		ua:      opts.UserAgent,
		cache:   make(map[string]cacheEntry),
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.ua == "" {
		c.ua = defaultUserAgent
	}
	return c
}

// get performs a GET with conditional-request caching and returns the body
// and response headers. An empty token makes an unauthenticated call.
func (c *Client) get(ctx context.Context, token, path string, query url.Values) ([]byte, http.Header, error) {
	full := c.baseURL + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	key := full + "\x00" + tokenHash(token)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("esi: build request: %w", err)
	}
	req.Header.Set("X-Compatibility-Date", compatibilityDate)
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	cached, haveCached := c.lookup(key)
	if haveCached {
		req.Header.Set("If-None-Match", cached.etag)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, redactErr(err, token)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && haveCached {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		return cached.body, cached.header, nil
	}
	body, err := readBody(resp, token)
	if err != nil {
		return nil, nil, err
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		c.store(key, cacheEntry{etag: etag, body: body, header: resp.Header.Clone()})
	}
	return body, resp.Header, nil
}

// readBody maps a non-2xx status to its error and otherwise returns the
// bounded response body.
func readBody(resp *http.Response, token string) ([]byte, error) {
	switch {
	case resp.StatusCode == 420 || resp.StatusCode == http.StatusTooManyRequests:
		return nil, &RateLimitError{Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, &APIError{Status: resp.StatusCode, Message: readErrorMessage(resp.Body, token)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, redactErr(err, token)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("esi: response exceeds %d bytes", maxBodyBytes)
	}
	return body, nil
}

// postJSON performs an unauthenticated POST of a JSON body. Responses are not
// cached.
func (c *Client) postJSON(ctx context.Context, path string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("esi: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("esi: build request: %w", err)
	}
	req.Header.Set("X-Compatibility-Date", compatibilityDate)
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readBody(resp, "")
}

func (c *Client) lookup(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	return e, ok
}

func (c *Client) store(key string, e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.cache[key]; !exists && len(c.cache) >= maxCacheEntries {
		for k := range c.cache { // evict an arbitrary entry
			delete(c.cache, k)
			break
		}
	}
	c.cache[key] = e
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// readErrorMessage reads a bounded error body and extracts ESI's "error"
// field when present. The token is scrubbed in case a server echoes it.
func readErrorMessage(r io.Reader, token string) string {
	raw, _ := io.ReadAll(io.LimitReader(r, maxErrorBodyBytes))
	msg := strings.TrimSpace(string(raw))
	var parsed struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &parsed) == nil && parsed.Error != "" {
		msg = parsed.Error
	}
	return scrub(msg, token)
}

func scrub(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}

// redactErr removes the token from a transport error, preserving the chain
// for errors.Is/As on context errors.
func redactErr(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return &redactedError{msg: scrub(err.Error(), token), err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// UniverseNames resolves ids to names with the public POST /universe/names,
// sending at most 1000 distinct ids per call. It sends no token. One invalid id
// makes ESI reject its whole batch, which is returned as the error.
func (c *Client) UniverseNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	seen := make(map[int64]bool, len(ids))
	var distinct []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			distinct = append(distinct, id)
		}
	}
	for len(distinct) > 0 {
		batch := distinct[:min(maxNameBatch, len(distinct))]
		distinct = distinct[len(batch):]
		body, err := c.postJSON(ctx, "/universe/names", batch)
		if err != nil {
			return nil, err
		}
		var rows []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &rows); err != nil {
			return nil, fmt.Errorf("esi: decode universe names: %w", err)
		}
		for _, r := range rows {
			out[r.ID] = r.Name
		}
	}
	return out, nil
}
