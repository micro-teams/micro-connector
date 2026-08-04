// Package apiauth resolves how this machine authenticates to its control plane, and hands out an
// http transport that applies it.
//
// By default a machine speaks as itself: its durable token goes out as a bearer credential. That is
// all a provisioning tool ever needs, and it is the default precisely because it is the smaller
// claim.
//
// A product whose screens act on behalf of somebody can opt into a second step. Set ScreenExchange,
// and inside a screen the machine's token plus the screen's token are exchanged, once and cached,
// for a token belonging to whoever that screen represents — so the thing in the terminal reaches
// the same guarded endpoints a person would, as that person, rather than borrowing the machine's
// authority. Left nil, none of that machinery exists.
//
// Environment overrides, under the brand's prefix: <PREFIX>_API (base URL), <PREFIX>_TOKEN
// (credential), <PREFIX>_SCREEN (per-screen token, set by the host inside every screen it opens).
package apiauth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/micro-teams/micro-connector/cli/brand"
	"github.com/micro-teams/micro-connector/cli/config"
)

// ScreenTokenExchange describes how a screen's own credential is obtained. A product that has no
// such notion leaves ScreenExchange nil and every request goes out as the machine.
type ScreenTokenExchange struct {
	// Path is the control-plane endpoint that performs the exchange, e.g. "/agent/token".
	Path string
	// SessionHeader carries the machine's durable token; ScreenHeader carries the screen's.
	SessionHeader string
	ScreenHeader  string
}

// ScreenExchange is the product's policy, set at startup beside the brand. nil means "the machine
// speaks as itself", which is the default and the safer of the two.
var ScreenExchange *ScreenTokenExchange

// APIBase returns the control plane's base URL: the brand's API variable if set, else this
// machine's configured base, else a localhost default.
func APIBase() string {
	if base := brand.Current.Getenv("API"); base != "" {
		return strings.TrimRight(base, "/")
	}
	if cfg, err := config.Load(config.DefaultPath()); err == nil && cfg.Base != "" {
		return cfg.APIBase()
	}
	return "http://localhost:8080"
}

// resolveToken returns this machine's credential: MICROTEAMS_TOKEN if set, else the stored one.
func resolveToken() string {
	if tok := brand.Current.Getenv("TOKEN"); tok != "" {
		return tok
	}
	if cfg, err := config.Load(config.DefaultPath()); err == nil {
		return cfg.Token
	}
	return ""
}

// Transport returns an http.RoundTripper that authenticates every request bound for the API host,
// sending it over the standard transport.
func Transport() http.RoundTripper { return TransportOver(nil) }

// TransportOver is Transport with the request finally issued by base rather than by the standard
// transport. nil means http.DefaultTransport.
//
// The seam exists because authentication is not the only thing a product may want to do to its
// outbound requests — one might route them over several network paths, another might record them —
// and none of that belongs in this library. Authentication stays outermost either way, which is the
// order that matters: this decides whether to attach a credential by looking at the host it was
// configured for, so anything that rewrites the host has to run underneath it, and anything that
// retries has to carry a request that is already authenticated.
func TransportOver(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	apiBase := APIBase()
	host := ""
	if u, err := url.Parse(apiBase); err == nil {
		host = u.Host
	}
	return &bearerInjector{
		base:    base,
		host:    host,
		apiBase: apiBase,
		token:   resolveToken(),
		screen:  brand.Current.Getenv("SCREEN"),
		exch:    ScreenExchange,
	}
}

// Client returns an http.Client that applies Transport() with a sane timeout.
func Client() *http.Client { return ClientOver(nil) }

// ClientOver is Client over a caller-supplied base transport. See TransportOver.
func ClientOver(base http.RoundTripper) *http.Client {
	return &http.Client{Transport: TransportOver(base), Timeout: 30 * time.Second}
}

type bearerInjector struct {
	base    http.RoundTripper
	host    string
	apiBase string // server base, for the /agent/token exchange
	token   string // this machine's durable token
	screen  string // per-screen token; non-empty only inside a screen
	exch    *ScreenTokenExchange

	mu       sync.Mutex
	agentJWT string
	agentExp time.Time
}

func (b *bearerInjector) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == b.host && req.Header.Get("Authorization") == "" {
		if b.screen != "" && b.exch != nil {
			if jwt := b.agentToken(); jwt != "" {
				req = req.Clone(req.Context())
				req.Header.Set("Authorization", "Bearer "+jwt)
			}
		} else if b.token != "" {
			req = req.Clone(req.Context())
			req.Header.Set("Authorization", "Bearer "+b.token)
		}
	}
	return b.base.RoundTrip(req)
}

// agentToken returns the screen's own token, exchanging the machine + screen tokens for a fresh one
// when the cache is empty or near expiry. Returns "" on failure — the call then goes out
// unauthenticated and the control plane answers 401, which surfaces as an ordinary API error rather
// than as something mysterious.
func (b *bearerInjector) agentToken() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.agentJWT != "" && time.Now().Before(b.agentExp.Add(-30*time.Second)) {
		return b.agentJWT
	}
	req, err := http.NewRequest(http.MethodPost, b.apiBase+b.exch.Path, nil)
	if err != nil {
		return ""
	}
	req.Header.Set(b.exch.SessionHeader, b.token)
	req.Header.Set(b.exch.ScreenHeader, b.screen)
	// Go through the underlying transport, not this injector, so the exchange cannot recurse.
	resp, err := b.base.RoundTrip(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Token == "" {
		return ""
	}
	b.agentJWT = out.Token
	b.agentExp = time.Unix(out.ExpiresAt, 0)
	return b.agentJWT
}
