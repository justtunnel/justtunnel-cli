package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// PlanInfo is defined in model.go — reused here for FetchPlanInfo.

// planLimits maps plan names to their maximum number of concurrent tunnels.
// This mirrors the server-side plan/limits.go configuration.
var planLimits = map[string]int{
	"free":    1,
	"starter": 2,
	"pro":     5,
}

// defaultMaxTunnels is the fallback limit for unknown plan names.
const defaultMaxTunnels = 1

// meResponsePlan is the subset of the /api/me response we need.
type meResponsePlan struct {
	Plan string `json:"plan"`
}

// FetchPlanInfo calls the /api/me endpoint to retrieve the user's plan
// and maps it to tunnel limits. The serverURL can be in any scheme
// (wss://, ws://, https://, http://) and will be converted appropriately.
func FetchPlanInfo(serverURL string, token string) (PlanInfo, error) {
	baseURL, err := apiBaseURL(serverURL)
	if err != nil {
		return PlanInfo{}, fmt.Errorf("parse server URL: %w", err)
	}

	request, err := http.NewRequest("GET", baseURL+"/api/me", nil)
	if err != nil {
		return PlanInfo{}, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return PlanInfo{}, fmt.Errorf("fetch plan info: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return PlanInfo{}, fmt.Errorf("fetch plan info: HTTP %d", response.StatusCode)
	}

	var meResp meResponsePlan
	if err := json.NewDecoder(response.Body).Decode(&meResp); err != nil {
		return PlanInfo{}, fmt.Errorf("decode plan response: %w", err)
	}

	return PlanInfo{
		Name:       meResp.Plan,
		MaxTunnels: maxTunnelsForPlan(meResp.Plan),
	}, nil
}

// maxTunnelsForPlan returns the tunnel limit for a given plan name.
// Unknown or empty plan names default to the free plan limit.
func maxTunnelsForPlan(planName string) int {
	if limit, ok := planLimits[planName]; ok {
		return limit
	}
	return defaultMaxTunnels
}

// apiBaseURL derives the REST API base URL from a WebSocket or HTTP server URL.
// e.g. "wss://api.justtunnel.dev/ws" -> "https://api.justtunnel.dev"
func apiBaseURL(serverURL string) (string, error) {
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	switch parsedURL.Scheme {
	case "wss":
		parsedURL.Scheme = "https"
	case "ws":
		parsedURL.Scheme = "http"
	}
	parsedURL.Path = ""
	parsedURL.RawQuery = ""
	return parsedURL.String(), nil
}
