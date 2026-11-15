package publish

import (
	"slices"
	"testing"
)

func TestRecords(t *testing.T) {
	l := List{Domains: []string{"http://localhost", "https://dev.example.com", "tls://dev.legacy.com"}, Ports: []int{3000, 3001}}
	records := l.Records()
	want := []string{"http localhost", "https dev.example.com", "tls dev.legacy.com", "ports 3000 3001"}
	if !slices.Equal(records, want) {
		t.Fatalf("records: %q", records)
	}
	back := Parse(append(records, "future thing", "https localhost", "ports x 70000 3000", "tls dev.legacy.com"))
	if !slices.Equal(back.Domains, l.Domains) || !slices.Equal(back.Ports, l.Ports) {
		t.Fatalf("parsed back, without what it does not understand, nor twice: %+v", back)
	}
}
