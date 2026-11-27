package checkout

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckoutFreezesServerCustomerDetailsWithoutExposingThemInJSON(t *testing.T) {
	s, _, _, actor, request, _ := checkoutFixture()
	actor.Contact = "79000000000"
	actor.CustomerName = "  Test Customer  "
	order, err := s.Prepare(context.Background(), actor, "server-contact", request)
	if err != nil {
		t.Fatal(err)
	}
	if order.CustomerContact != actor.Contact || order.CustomerName != "Test Customer" {
		t.Fatal("server customer details were not retained")
	}
	body, err := json.Marshal(order)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), actor.Contact) || strings.Contains(string(body), order.CustomerName) {
		t.Fatal("private customer details leaked in checkout response")
	}
	actor.Contact = "79111111111"
	actor.CustomerName = "Renamed Customer"
	replayed, err := s.Prepare(context.Background(), actor, "server-contact", request)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.CustomerContact != order.CustomerContact || replayed.CustomerName != order.CustomerName {
		t.Fatal("a retried checkout changed its frozen customer details")
	}
}
