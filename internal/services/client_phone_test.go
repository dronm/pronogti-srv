package services

import (
	"testing"

	"github.com/dronm/goyclients"
)

func TestPhoneLookupRequiresExactAndUnambiguousMatch(t *testing.T) {
	const phone = "79991234567"
	clients := []*goyclients.Client{
		nil,
		{ID: 1, Phone: "79991230000"},
		{ID: 2, Phone: "+7 (999) 123-45-67"},
	}
	matched, err := exactPhoneClient(clients, phone)
	if err != nil || matched.ID != 2 {
		t.Fatalf("matched=%+v error=%v", matched, err)
	}
	if matched, err := exactPhoneClient(clients[:2], phone); err != nil || matched.ID != 0 {
		t.Fatalf("fuzzy match accepted: %+v %v", matched, err)
	}
	clients = append(clients, &goyclients.Client{ID: 3, Phone: phone})
	if _, err := exactPhoneClient(clients, phone); err == nil {
		t.Fatal("ambiguous phone accepted")
	}
}
