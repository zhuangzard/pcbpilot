package app

import (
	"strings"
	"testing"
)

func TestDSNViaLargestFirst(t *testing.T) {
	dsn := "(pcb x (structure (layer F.Cu) (via \"Via[0-5]_600:300_um\" \"Via[0-5]_609.6:304.8_um\" \"Via[0-5]_700.024:400.05_um\" \"Via[0-5]_700.024:350_um\")\n (rule (width 200))) (library (padstack \"Via[0-5]_600:300_um\")))"
	out, order := dsnViaLargestFirst(dsn)
	if strings.Join(order, " ") != `"Via[0-5]_700.024:400.05_um" "Via[0-5]_700.024:350_um" "Via[0-5]_609.6:304.8_um" "Via[0-5]_600:300_um"` {
		t.Fatalf("order %v", order)
	}
	if !strings.Contains(out, `(via "Via[0-5]_700.024:400.05_um" "Via[0-5]_700.024:350_um" "Via[0-5]_609.6:304.8_um" "Via[0-5]_600:300_um")`) || !strings.Contains(out, `(padstack "Via[0-5]_600:300_um")`) {
		t.Fatalf("dsn %s", out)
	}
	// An unreadable size leaves the DSN alone.
	odd := `(pcb x (structure (via "MyVia" "Via[0-5]_600:300_um")))`
	if got, o := dsnViaLargestFirst(odd); got != odd || o != nil {
		t.Fatalf("changed %s", got)
	}
}
