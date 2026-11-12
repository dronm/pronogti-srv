package fiscal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"
)

// DriverConnection binds an exact receipt-service destination to one optional
// secret name. The actual secret is supplied by the process environment.
type DriverConnection struct {
	BaseURL        string `json:"base_url"`
	BearerTokenEnv string `json:"bearer_token_env"`
}

func ValidDriverBaseURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" &&
		u.User == nil && u.RawQuery == "" && !u.ForceQuery && !strings.Contains(value, "#") &&
		u.Fragment == "" && u.RawPath == "" && u.Opaque == "" &&
		!strings.ContainsFunc(value, unicode.IsSpace)
}

func ValidBearerTokenEnv(value string) bool {
	return envNamePattern.MatchString(value)
}

// ParseAllowedDriverConnections reads the deployment's trusted URL and token
// pairs. An unset value permits retaining existing connections and clearing a
// demo profile, but grants no new destination or secret.
func ParseAllowedDriverConnections(value string) ([]DriverConnection, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	var connections []DriverConnection
	if decoder.Decode(&connections) != nil || connections == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("expected a JSON array of objects with base_url and optional bearer_token_env")
	}
	for i, connection := range connections {
		if !ValidDriverBaseURL(connection.BaseURL) {
			return nil, fmt.Errorf("connection %d has an invalid base_url", i+1)
		}
		if connection.BearerTokenEnv != "" && !ValidBearerTokenEnv(connection.BearerTokenEnv) {
			return nil, fmt.Errorf("connection %d has an invalid bearer_token_env", i+1)
		}
	}
	return connections, nil
}
