// Package auth is the machine's half of enrolment. The machine asks the control plane to start an
// authorization, shows whoever is at the machine a link to approve it, and polls until the control
// plane answers with the machine's credential. How the approval happens is the control plane's
// business.
//
//	POST <base><EnrollBase>/start  {}            -> {"code", "approveUrl", "interval"}
//	                                                 (interval: seconds between polls, 2 when absent)
//	POST <base><EnrollBase>/poll   {"code"}      -> {"status": "pending" | "approved" | "denied",
//	                                                 "token", "machineId"}
//
//	Login(ctx, base, show func(approveURL string)) (*Result, error)
//	    runs the exchange; show is called once with the link; returns when approved, denied,
//	    failed or ctx ends.
//	Result{Token, MachineID}
//
// Example:
//
//	res, err := auth.Login(ctx, "https://example.com", func(u string) { fmt.Println("approve at", u) })
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/micro-teams/micro-connector/cli/brand"
)

// defaultInterval is the poll interval the protocol gives a start response without one.
const defaultInterval = 2 * time.Second

type startResp struct {
	Code       string `json:"code"`
	ApproveURL string `json:"approveUrl"`
	Interval   int    `json:"interval"`
}

type pollResp struct {
	Status    string `json:"status"`
	Token     string `json:"token"`
	MachineID string `json:"machineId"`
}

type Result struct {
	Token     string
	MachineID string
}

func Login(ctx context.Context, base string, show func(approveURL string)) (*Result, error) {
	base = strings.TrimRight(base, "/")
	var sr startResp
	if err := postJSON(ctx, base+brand.Current.EnrollBase+"/start", map[string]string{}, &sr); err != nil {
		return nil, fmt.Errorf("auth: start: %w", err)
	}
	if sr.Code == "" || sr.ApproveURL == "" || sr.Interval < 0 {
		return nil, fmt.Errorf("auth: incomplete start response %+v", sr)
	}
	show(sr.ApproveURL)
	interval := time.Duration(sr.Interval) * time.Second
	if sr.Interval == 0 {
		interval = defaultInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
			var pr pollResp
			if err := postJSON(ctx, base+brand.Current.EnrollBase+"/poll",
				map[string]string{"code": sr.Code}, &pr); err != nil {
				return nil, fmt.Errorf("auth: poll: %w", err)
			}
			switch pr.Status {
			case "approved":
				if pr.Token == "" {
					return nil, fmt.Errorf("auth: approved but no token returned")
				}
				return &Result{Token: pr.Token, MachineID: pr.MachineID}, nil
			case "denied":
				return nil, fmt.Errorf("auth: the request was denied")
			case "pending":
			default:
				return nil, fmt.Errorf("auth: unknown status %q", pr.Status)
			}
		}
	}
}

func newJSONRequest(ctx context.Context, url string, body any) (*http.Request, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func postJSON(ctx context.Context, url string, body, out any) error {
	req, err := newJSONRequest(ctx, url, body)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
