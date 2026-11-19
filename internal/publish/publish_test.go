package publish

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"slices"
	"strconv"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
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

// The server here answers over TCP only, as Docker Desktop leaves a Mac's waggle no other way to its VPN DNS.
func TestLookupOverTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serveTXT(ln, []string{"http localhost", "ports 3000"})
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	got, err := Lookup(context.Background(), host, p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Domains, []string{"http://localhost"}) || !slices.Equal(got.Ports, []int{3000}) {
		t.Fatalf("list: %+v", got)
	}
}

// serveTXT answers each query with records, in DNS over TCP: a two-byte length, then the message.
func serveTXT(ln net.Listener, records []string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			var size [2]byte
			if _, err := io.ReadFull(c, size[:]); err != nil {
				return
			}
			query := make([]byte, binary.BigEndian.Uint16(size[:]))
			if _, err := io.ReadFull(c, query); err != nil {
				return
			}
			var parser dnsmessage.Parser
			h, err := parser.Start(query)
			if err != nil {
				return
			}
			q, err := parser.Question()
			if err != nil {
				return
			}
			b := dnsmessage.NewBuilder(make([]byte, 2, 512), dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true})
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			for _, r := range records {
				rh := dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 60}
				_ = b.TXTResource(rh, dnsmessage.TXTResource{TXT: []string{r}})
			}
			msg, err := b.Finish()
			if err != nil {
				return
			}
			binary.BigEndian.PutUint16(msg, uint16(len(msg)-2))
			_, _ = c.Write(msg)
		}()
	}
}
