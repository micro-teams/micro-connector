// Tests for the authenticating transport: the transport underneath receives a request that already
// carries the credential (so one that rewrites the host cannot lose it), a foreign host gets no
// credential, and missing configuration or a failed exchange is an error.
package apiauth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// recorder stands in for whatever a product puts underneath: it remembers what it was handed, may
// rewrite the destination, and answers without a network.
type recorder struct {
	seen    *http.Request
	rewrite string // non-empty: send it somewhere else, as a line-selecting transport would
	status  int    // the answer; 200 when zero
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
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode: status,
		Body:       http.NoBody,
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func TestTheBaseTransportSeesAnAuthenticatedRequest(t *testing.T) {
	t.Setenv("CONNECTOR_API", "https://control.example")
	t.Setenv("CONNECTOR_TOKEN", "machine-token")

	base := &recorder{}
	client := mustClient(t, base)

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
	if _, err := mustClient(t, base).Get("https://control.example/chat"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := base.seen.Header.Get("Authorization"); got != "Bearer machine-token" {
		t.Errorf("credential missing before the rewrite: %q", got)
	}
}

func mustClient(t *testing.T, base http.RoundTripper) *http.Client {
	t.Helper()
	c, err := ClientOver(base)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNoBaseAnywhereIsAnError(t *testing.T) {
	t.Setenv("CONNECTOR_API", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	if _, err := Transport(); err == nil {
		t.Fatal("a transport with no control plane configured")
	}
}

func TestAFailedExchangeFailsTheRequest(t *testing.T) {
	t.Setenv("CONNECTOR_API", "https://control.example")
	t.Setenv("CONNECTOR_TOKEN", "machine-token")
	t.Setenv("CONNECTOR_SCREEN", "screen-token")
	ScreenExchange = &ScreenTokenExchange{Path: "/token", SessionHeader: "X-S", ScreenHeader: "X-Sc"}
	defer func() { ScreenExchange = nil }()
	base := &recorder{status: http.StatusForbidden}
	_, err := mustClient(t, base).Get("https://control.example/chat")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the exchange's 403", err)
	}
}

// A request to somewhere else is not ours to authenticate — the machine's credential must not leak
// to a third party just because it went through this client.
func TestAForeignHostGetsNoCredential(t *testing.T) {
	t.Setenv("CONNECTOR_API", "https://control.example")
	t.Setenv("CONNECTOR_TOKEN", "machine-token")

	base := &recorder{}
	if _, err := mustClient(t, base).Get("https://somewhere.else/thing"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := base.seen.Header.Get("Authorization"); got != "" {
		t.Errorf("the machine's token was sent to another host: %q", got)
	}
}
