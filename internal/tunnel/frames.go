package tunnel

import (
	"encoding/json"
	"fmt"
)

type TunnelAssigned struct {
	Type              string `json:"type"`
	TunnelID          string `json:"tunnel_id"`
	Subdomain         string `json:"subdomain"`
	URL               string `json:"url"`
	ReconnectToken    string `json:"reconnect_token,omitempty"`
	PasswordProtected bool   `json:"password_protected,omitempty"`
}

type RequestFrame struct {
	Type    string              `json:"type"`
	ID      string              `json:"id"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}

type ResponseFrame struct {
	Type    string              `json:"type"`
	ID      string              `json:"id"`
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}

type ErrorFrame struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Message string `json:"message"`
}

type frameEnvelope struct {
	Type string `json:"type"`
}

// ParseFrame reads the type field from a raw JSON message and unmarshals
// into the appropriate typed struct.
func ParseFrame(data []byte) (any, error) {
	var env frameEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse frame type: %w", err)
	}

	switch env.Type {
	case "tunnel_assigned":
		var assigned TunnelAssigned
		if err := json.Unmarshal(data, &assigned); err != nil {
			return nil, fmt.Errorf("unmarshal tunnel_assigned: %w", err)
		}
		return &assigned, nil
	case "request":
		var reqFrame RequestFrame
		if err := json.Unmarshal(data, &reqFrame); err != nil {
			return nil, fmt.Errorf("unmarshal request: %w", err)
		}
		return &reqFrame, nil
	case "response":
		var respFrame ResponseFrame
		if err := json.Unmarshal(data, &respFrame); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}
		return &respFrame, nil
	case "error":
		var errFrame ErrorFrame
		if err := json.Unmarshal(data, &errFrame); err != nil {
			return nil, fmt.Errorf("unmarshal error: %w", err)
		}
		return &errFrame, nil
	default:
		return nil, fmt.Errorf("unknown frame type: %q", env.Type)
	}
}
