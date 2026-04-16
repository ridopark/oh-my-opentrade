package httputil

import "net/http"

// HTTPDoer is the subset of *http.Client used by adapters for testability.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}
