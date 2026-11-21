// Package apiauth authenticates a machine's requests to its control plane: an http transport that
// adds the credential to every request bound for the control plane's host.
//
// By default a machine speaks as itself: its token is the bearer credential. A product whose
// screens act for somebody sets ScreenExchange; inside a screen the machine's token and the
// screen's token are then exchanged (once, cached until shortly before expiry) for a token of
// whoever the screen represents, so a program in the terminal reaches the control plane as that
// person rather than with the machine's authority.
//
// Environment, under the brand's prefix: <PREFIX>_API (base URL), <PREFIX>_TOKEN (credential),
// <PREFIX>_SCREEN (the screen's token, which the connector sets in every screen).
//
//	ScreenTokenExchange{Path, SessionHeader, ScreenHeader}  the exchange endpoint and the headers
//	                       carrying the machine's and the screen's tokens; answered with
//	                       {"token", "expiresAt" (unix seconds)}.
//	ScreenExchange         the product's exchange, or nil.
//	APIBase() (string, error)    <PREFIX>_API, else the configured base; an error when neither.
//	Transport() / TransportOver(base http.RoundTripper) (http.RoundTripper, error)
//	                       the authenticating transport, over http.DefaultTransport or base (which
//	                       issues the request; anything that rewrites hosts or retries goes there).
//	Client() / ClientOver(base) (*http.Client, error)  the same in a client with a 30 s timeout.
//
// A failed exchange fails the request with its reason; nothing is sent unauthenticated.
//
// Example:
//
//	apiauth.ScreenExchange = &apiauth.ScreenTokenExchange{Path: "/agent/token",
//		SessionHeader: "X-Session", ScreenHeader: "X-Screen"}
//	c, err := apiauth.Client()
//	resp, err := c.Get(base + "/me")
package apiauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/micro-teams/micro-connector/cli/brand"
	"github.com/micro-teams/micro-connector/cli/config"
)

type ScreenTokenExchange struct {
	Path          string
	SessionHeader string
	ScreenHeader  string
}

var ScreenExchange *ScreenTokenExchange

func APIBase() (string, error) {
	if base := brand.Current.Getenv("API"); base != "" {
		return strings.TrimRight(base, "/"), nil
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return "", fmt.Errorf("apiauth: no %s and no configuration: %w", brand.Current.Env("API"), err)
	}
	if cfg.Base == "" {
		return "", errors.New("apiauth: the configuration has no base URL")
	}
	return cfg.APIBase(), nil
}

func resolveToken() (string, error) {
	if tok := brand.Current.Getenv("TOKEN"); tok != "" {
		return tok, nil
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return "", fmt.Errorf("apiauth: no %s and no configuration: %w", brand.Current.Env("TOKEN"), err)
	}
	return cfg.Token, nil
}

func Transport() (http.RoundTripper, error) { return TransportOver(nil) }

func TransportOver(base http.RoundTripper) (http.RoundTripper, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	apiBase, err := APIBase()
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(apiBase)
	if err != nil {
		return nil, fmt.Errorf("apiauth: base %q: %w", apiBase, err)
	}
	token, err := resolveToken()
	if err != nil {
		return nil, err
	}
	return &bearerInjector{
		base:    base,
		host:    u.Host,
		apiBase: apiBase,
		token:   token,
		screen:  brand.Current.Getenv("SCREEN"),
		exch:    ScreenExchange,
	}, nil
}

func Client() (*http.Client, error) { return ClientOver(nil) }

func ClientOver(base http.RoundTripper) (*http.Client, error) {
	t, err := TransportOver(base)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: t, Timeout: 30 * time.Second}, nil
}

type bearerInjector struct {
	base    http.RoundTripper
	host    string
	apiBase string
	token   string
	screen  string
	exch    *ScreenTokenExchange

	mu       sync.Mutex
	agentJWT string
	agentExp time.Time
}

func (b *bearerInjector) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != b.host || req.Header.Get("Authorization") != "" {
		return b.base.RoundTrip(req)
	}
	cred := b.token
	if b.screen != "" && b.exch != nil {
		jwt, err := b.agentToken()
		if err != nil {
			return nil, err
		}
		cred = jwt
	}
	if cred != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+cred)
	}
	return b.base.RoundTrip(req)
}

// agentToken returns the screen's own token, exchanging for a fresh one when the cached one is
// missing or within 30 s of expiry. The exchange goes through the base transport, not this one.
func (b *bearerInjector) agentToken() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.agentJWT != "" && time.Now().Before(b.agentExp.Add(-30*time.Second)) {
		return b.agentJWT, nil
	}
	req, err := http.NewRequest(http.MethodPost, b.apiBase+b.exch.Path, nil)
	if err != nil {
		return "", fmt.Errorf("apiauth: token exchange: %w", err)
	}
	req.Header.Set(b.exch.SessionHeader, b.token)
	req.Header.Set(b.exch.ScreenHeader, b.screen)
	resp, err := b.base.RoundTrip(req)
	if err != nil {
		return "", fmt.Errorf("apiauth: token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("apiauth: token exchange: %s", resp.Status)
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		return "", fmt.Errorf("apiauth: token exchange: unreadable answer (%v)", err)
	}
	b.agentJWT = out.Token
	b.agentExp = time.Unix(out.ExpiresAt, 0)
	return b.agentJWT, nil
}
