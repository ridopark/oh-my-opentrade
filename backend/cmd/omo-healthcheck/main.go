// Package main implements a tiny HTTP health probe binary. It is intended
// to be baked into the omo-core distroless image so docker-compose's
// HEALTHCHECK directive has an executable to invoke.
//
// The distroless/static base image has no shell, no curl, and no wget, so
// the conventional "curl -f http://localhost/healthz || exit 1" pattern
// cannot work. This 30-line program is the minimal thing that does.
//
// Exit codes:
//
//	0 — endpoint returned 2xx
//	1 — any other state (non-2xx, network error, timeout, unexpected body)
//
// Usage: omo-healthcheck [url]
// Default URL: http://127.0.0.1:8080/healthz
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	url := "http://127.0.0.1:8080/healthz"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "omo-healthcheck: build request: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "omo-healthcheck: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "omo-healthcheck: non-2xx status %d\n", resp.StatusCode)
		os.Exit(1)
	}
}
