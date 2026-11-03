package checkout

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckoutFreezesServerContactWithoutExposingItInJSON(t *testing.T) {
	s, _, _, actor, request, _ := checkoutFixture()
	actor.Contact = "79000000000"
	order, err := s.Prepare(context.Background(), actor, "server-contact", request)
	if err != nil {
		t.Fatal(err)
	}
	if order.CustomerContact != actor.Contact {
		t.Fatal("server contact was not retained for independent receipt processing")
	}
	body, err := json.Marshal(order)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), actor.Contact) || strings.Contains(string(body), "customer_contact") {
		t.Fatal("private receipt contact leaked in checkout response")
	}
	actor.Contact = "79111111111"
	replayed, err := s.Prepare(context.Background(), actor, "server-contact", request)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.CustomerContact != order.CustomerContact {
		t.Fatal("a retried checkout changed its frozen receipt contact")
	}
}
