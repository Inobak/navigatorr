package openapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jakenesler/navigatorr/internal"
)

var fetchClient = &http.Client{Timeout: 30 * time.Second}

// maxSpecSize is the maximum number of bytes read from an OpenAPI spec response.
const maxSpecSize = 50 * 1024 * 1024 // 50 MB

// Fetch downloads an OpenAPI spec, using cache if available.
func Fetch(ctx context.Context, url string, cache *Cache) ([]byte, error) {
	// Try cache first
	if data := cache.Get(url); data != nil {
		return data, nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json, application/x-yaml, text/yaml")

	resp, err := fetchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching spec from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetching spec from %s: HTTP %d", url, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSpecSize))
	if err != nil {
		return nil, fmt.Errorf("reading spec: %w", err)
	}

	// Cache to disk
	if err := cache.Put(url, data); err != nil {
		// Non-fatal, just log
		internal.Errorf("failed to cache spec: %v", err)
	}

	return data, nil
}
