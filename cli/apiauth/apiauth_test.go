// The seam under the credential.
//
// What these pin is an ordering, not a feature: whatever a product puts underneath this transport
// receives a request that is already authenticated, and is free to change where that request goes.
// Get it the other way round — decide the credential after something has rewritten the host — and
// the credential is silently dropped, because this injector attaches it by comparing the host
// against the API it was configured for. A test is worth it precisely because that failure looks
// like an auth bug rather than a layering one.

package apiauth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// recorder stands in for whatever a product puts underneath: it remembers what it was handed, may
// rewrite the destination, and answers without a network.
type recorder struct {
	seen    *http.Request
	rewrite string // non-empty: send it somewhere else, as a line-selecting transport would
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.seen = req
	if r.rewrite != "" {
		clone := req.Clone(req.Context())
		u, err := url.Parse(r.rewrite + req.URL.Path)
		if err != nil {
			return nil, err
		}
		clone.URL = u
		req = clone
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func TestTheBaseTransportSeesAnAuthenticatedRequest(t *testing.T) {
	t.Setenv("CONNECTOR_API", "https://control.example")
	t.Setenv("CONNECTOR_TOKEN", "machine-token")

	base := &recorder{}
	client := ClientOver(base)

	if _, err := client.Get("https://control.example/chat"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if base.seen == nil {
		t.Fatal("the base transport was never reached")
	}
	if got := base.seen.Header.Get("Authorization"); got != "Bearer machine-token" {
		t.Errorf("the credential had not been attached yet: %q", got)
	}
}

// The case this seam exists for: something underneath sends the request to a different host. The
// credential must already be on it, because by then nothing can tell that this was an API call.
func TestABaseThatRewritesTheHostStillCarriesTheCredential(t *testing.T) {
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }),
	)
	defer server.Close()

	t.Setenv("CONNECTOR_API", "https://control.example")
	t.Setenv("CONNECTOR_TOKEN", "machine-token")

	base := &recorder{rewrite: server.URL}
	if _, err := ClientOver(base).Get("https://control.example/chat"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := base.seen.Header.Get("Authorization"); got != "Bearer machine-token" {
		t.Errorf("credential missing before the rewrite: %q", got)
	}
}

// And the default stays the default: no argument means the standard transport, which is what every
// existing caller gets.
func TestTransportWithoutABaseIsStillATransport(t *testing.T) {
	t.Setenv("CONNECTOR_API", "https://control.example")
	if Transport() == nil {
		t.Fatal("Transport() returned nil")
	}
	if TransportOver(nil) == nil {
		t.Fatal("TransportOver(nil) returned nil")
	}
}

// A request to somewhere else is not ours to authenticate — the machine's credential must not leak
// to a third party just because it went through this client.
func TestAForeignHostGetsNoCredential(t *testing.T) {
	t.Setenv("CONNECTOR_API", "https://control.example")
	t.Setenv("CONNECTOR_TOKEN", "machine-token")

	base := &recorder{}
	if _, err := ClientOver(base).Get("https://somewhere.else/thing"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := base.seen.Header.Get("Authorization"); got != "" {
		t.Errorf("the machine's token was sent to another host: %q", got)
	}
}
