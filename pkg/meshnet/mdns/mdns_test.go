package mdns

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type hub = MemNetwork

func (h *hub) join(ip string) Conn { return h.Join(ip) }

func advert() Advert {
	return Advert{Instance: "pop-os", Port: 17380, Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.20")},
		TXT: map[string]string{"v": "1", "n": "pop-os", "p": "linux", "fp": "ABCD-EFGH-IJKL-MNOP"}}
}

func TestAdvertiseAndBrowse(t *testing.T) {
	h := &hub{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adDone := make(chan error, 1)
	go func() { adDone <- Advertise(ctx, h.join("192.168.1.20"), advert()) }()
	bctx, bcancel := context.WithTimeout(ctx, 5*time.Second)
	defer bcancel()
	found, err := Browse(bctx, h.join("192.168.1.30"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-found:
		if e.Instance != "pop-os" || e.Port != 17380 || len(e.Addrs) != 1 || e.Addrs[0].String() != "192.168.1.20" || e.TXT["fp"] != "ABCD-EFGH-IJKL-MNOP" || e.TXT["p"] != "linux" ||
			len(e.SelfAddrs()) != 1 {
			t.Fatalf("found %+v", e)
		}
	case <-bctx.Done():
		t.Fatal("nothing found")
	}
	cancel()
	if err := <-adDone; err != nil {
		t.Fatal(err)
	}
}

// Only _mads._tcp is listened for: another service's advert (a printer's)
// never appears, nor does a goodbye.
func TestBrowseIgnoresOtherServices(t *testing.T) {
	st := &browseState{entries: map[string]*Entry{}, hosts: map[string][]netip.Addr{}, sent: map[string]bool{}}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true})
	_ = b.StartAnswers()
	ipp := dnsmessage.MustNewName("_ipp._tcp.local.")
	printer := dnsmessage.MustNewName("printer._ipp._tcp.local.")
	_ = b.PTRResource(dnsmessage.ResourceHeader{Name: ipp, Class: dnsmessage.ClassINET, TTL: 120}, dnsmessage.PTRResource{PTR: printer})
	_ = b.SRVResource(dnsmessage.ResourceHeader{Name: printer, Class: dnsmessage.ClassINET, TTL: 120}, dnsmessage.SRVResource{Target: dnsmessage.MustNewName("printer.local."), Port: 631})
	_ = b.TXTResource(dnsmessage.ResourceHeader{Name: printer, Class: dnsmessage.ClassINET, TTL: 120}, dnsmessage.TXTResource{TXT: []string{"x=1"}})
	pkt, _ := b.Finish()
	if got := st.read(pkt, netip.MustParseAddr("192.168.1.50")); len(got) != 0 {
		t.Fatalf("a printer appeared: %+v", got)
	}
	bye, _ := advert().response(0)
	if got := st.read(bye, netip.MustParseAddr("192.168.1.20")); len(got) != 0 {
		t.Fatalf("a goodbye appeared: %+v", got)
	}
	// Garbage, truncation and queries are dropped without fuss.
	good, _ := advert().response(120)
	for _, p := range [][]byte{nil, {1, 2, 3}, good[:len(good)/2]} {
		_ = st.read(p, netip.Addr{})
	}
	q, _ := query()
	if got := st.read(q, netip.Addr{}); len(got) != 0 {
		t.Fatal("a query read as an answer")
	}
}

// A flood of fake adverts can't grow a browse without end.
func TestBrowseBounded(t *testing.T) {
	st := &browseState{entries: map[string]*Entry{}, hosts: map[string][]netip.Addr{}, sent: map[string]bool{}}
	for i := 0; i < 500; i++ {
		a := advert()
		a.Instance = "fake-" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
		pkt, err := a.response(120)
		if err != nil {
			t.Fatal(err)
		}
		st.read(pkt, netip.MustParseAddr("192.168.1.66"))
	}
	if len(st.entries) > maxEntries || len(st.hosts) > maxEntries {
		t.Fatalf("%d entries, %d hosts", len(st.entries), len(st.hosts))
	}
}

func TestAnswersOnlyItsQuestions(t *testing.T) {
	a := advert()
	q, _ := query()
	if !asksForUs(q, a) {
		t.Fatal("the service query")
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	_ = b.StartQuestions()
	_ = b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("_ipp._tcp.local."), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET})
	other, _ := b.Finish()
	if asksForUs(other, a) {
		t.Fatal("answered another service's query")
	}
	resp, _ := a.response(120)
	if asksForUs(resp, a) {
		t.Fatal("a response read as a query")
	}
	if escapeLabel("pop.os\\x") != `pop\.os\\x` || unescapeLabel(`pop\.os`) != "pop.os" || hostLabel("Drew's Mac!") != "drew-s-mac" {
		t.Fatal("labels")
	}
}

// An advert pointing at another machine's address (A records that aren't
// the sender's) offers nothing to dial.
func TestSelfAddrsOnlyTheSender(t *testing.T) {
	e := Entry{Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.99")}, From: netip.MustParseAddr("192.168.1.66")}
	if len(e.SelfAddrs()) != 0 {
		t.Fatal("an advert for someone else's address")
	}
	e.Addrs = append(e.Addrs, netip.MustParseAddr("192.168.1.66"))
	if got := e.SelfAddrs(); len(got) != 1 || got[0].String() != "192.168.1.66" {
		t.Fatalf("%v", got)
	}
}
