package fiscal

import "testing"

func TestParseAllowedDriverConnections(t *testing.T) {
	connections, err := ParseAllowedDriverConnections(`[
		{"base_url":"http://127.0.0.1:8090"},
		{"base_url":"https://driver.example.test/receipts","bearer_token_env":"TERMINALFA_TOKEN"}
	]`)
	if err != nil || len(connections) != 2 ||
		connections[0] != (DriverConnection{BaseURL: "http://127.0.0.1:8090"}) ||
		connections[1] != (DriverConnection{BaseURL: "https://driver.example.test/receipts", BearerTokenEnv: "TERMINALFA_TOKEN"}) {
		t.Fatalf("unexpected driver connections: %+v, %v", connections, err)
	}
	for _, input := range []string{
		`null`,
		`[{}]`,
		`[{"base_url":"http://driver.example.test","bearer_token":"secret"}]`,
		`[{"base_url":"http://operator:secret@driver.example.test"}]`,
		`[{"base_url":"http://driver.example.test?"}]`,
		`[{"base_url":"http://driver.example.test#"}]`,
		`[{"base_url":"http://driver.example.test/has space"}]`,
		`[{"base_url":"http://driver.example.test","bearer_token_env":"invalid-name"}]`,
		`[] []`,
	} {
		if _, err := ParseAllowedDriverConnections(input); err == nil {
			t.Errorf("accepted invalid driver allowlist: %s", input)
		}
	}
}
