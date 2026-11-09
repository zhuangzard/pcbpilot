package kicad

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const recC1 = `{"success":true,"result":{"title":"T","description":"10KΩ (1002) ±1%","lcsc":{"number":"C1","stock":5,"url":"u"},"dataStr":{"head":{"c_para":{"package":"R0402","Value":"10kΩ","Manufacturer Part":"M1"}}}}}`

// serve answers the n-th request with replies[n] (the last one repeats).
func serve(t *testing.T, replies ...string) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i >= len(replies) {
			i = len(replies) - 1
		}
		w.Write([]byte(replies[i]))
	}))
	t.Cleanup(s.Close)
	return s, &n
}

func TestLookupEasyEDAPart(t *testing.T) {
	ctx := context.Background()
	look := func(hosts ...string) (EasyEDAPart, bool, error) {
		return lookupEasyEDAPart(ctx, http.DefaultClient, hosts, time.Millisecond, "C1")
	}
	// Empty body and a null result are retried, then the record counts.
	s, _ := serve(t, "", `{"success":true,"result":null}`, recC1)
	if p, found, err := look(s.URL); err != nil || !found || p.Package != "R0402" || p.MPN != "M1" || p.Stock != 5 {
		t.Fatalf("retry: %+v %v %v", p, found, err)
	}
	// Not found only when every host says so.
	nf, _ := serve(t, `{"success":false,"message":"none"}`)
	nf2, _ := serve(t, `{"success":false}`)
	if _, found, err := look(nf.URL, nf2.URL); found || err != nil {
		t.Fatalf("not found: %v %v", found, err)
	}
	// One host says none, the other never answers usefully: unknown, an error.
	bad, _ := serve(t, "")
	if _, found, err := look(nf.URL, bad.URL); found || err == nil {
		t.Fatalf("half answer: %v %v", found, err)
	}
	// One host says none, the other has the record: found.
	ok, _ := serve(t, recC1)
	if _, found, err := look(nf.URL, ok.URL); !found || err != nil {
		t.Fatalf("second host: %v %v", found, err)
	}
	// A record of another number, or with no number, is not this part.
	other, _ := serve(t, `{"success":true,"result":{"lcsc":{"number":"C2"},"dataStr":{"head":{"c_para":{}}}}}`)
	if _, found, err := look(other.URL); found || err == nil {
		t.Fatalf("other number: %v %v", found, err)
	}
	anon, _ := serve(t, `{"success":true,"result":{}}`)
	if _, found, err := look(anon.URL); found || err == nil {
		t.Fatalf("no identity: %v %v", found, err)
	}
	// The context bounds the whole lookup.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := lookupEasyEDAPart(cctx, http.DefaultClient, []string{bad.URL}, time.Hour, "C1"); err == nil {
		t.Fatal("cancelled lookup returned no error")
	}
}
