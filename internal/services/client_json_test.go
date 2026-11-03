package services

import (
	"encoding/json"
	"testing"

	"github.com/dronm/goyclients"
)

func TestClientPointerJSONPreservesDateRoundTrip(t *testing.T) {
	for _, date := range []string{"1990-04-05", "0001-01-01"} {
		var client goyclients.Client
		if err := json.Unmarshal([]byte(`{"id":101,"birth_date":"`+date+`"}`), &client); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(&client)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if string(fields["birth_date"]) != `"`+date+`"` {
			t.Fatalf("birth_date lost its JSON date representation: %s", fields["birth_date"])
		}
		var restored goyclients.Client
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatalf("client cannot be decoded from cache: %v", err)
		}
		if restored.ID != client.ID {
			t.Fatal("client identity changed during cache round trip")
		}
	}
}
