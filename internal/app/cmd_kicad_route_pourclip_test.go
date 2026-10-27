package app

import (
	"reflect"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// The intent the pour clip reads names nets by display name; the board
// names them hierarchically. Every net reference is resolved, the input
// is not changed, unknown names stay.
func TestPcbautoIntentOnBoard(t *testing.T) {
	in := &pcbauto.Intent{
		Domains:    []pcbauto.IntentDomain{{ID: "P", Nets: []string{"VCC_PIC", "GND_P", "NOPE"}, Reference: "GND_P"}},
		NetClasses: []pcbauto.IntentNetClass{{Name: "PWR", Nets: []string{"VCC_PIC"}}},
		Nets:       map[string]*pcbauto.IntentNet{"VCC_PIC": {Role: "power", FloatsOn: "GND_P"}, "D_P": {DiffPair: "D_N"}},
	}
	board := []string{"/pic/VCC_PIC", "/pic/GND_P", "/usb/D_P", "/usb/D_N"}
	got := pcbautoIntentOnBoard(in, board)
	if want := []string{"/pic/VCC_PIC", "/pic/GND_P", "NOPE"}; !reflect.DeepEqual(got.Domains[0].Nets, want) {
		t.Errorf("domain nets %v, want %v", got.Domains[0].Nets, want)
	}
	if got.Domains[0].Reference != "/pic/GND_P" || got.NetClasses[0].Nets[0] != "/pic/VCC_PIC" {
		t.Errorf("reference %q, class nets %v", got.Domains[0].Reference, got.NetClasses[0].Nets)
	}
	if n := got.Nets["/pic/VCC_PIC"]; n == nil || n.FloatsOn != "/pic/GND_P" {
		t.Errorf("nets %v", got.Nets)
	}
	if n := got.Nets["/usb/D_P"]; n == nil || n.DiffPair != "/usb/D_N" {
		t.Errorf("diff pair: %v", got.Nets)
	}
	if in.Domains[0].Nets[0] != "VCC_PIC" || in.Nets["VCC_PIC"].FloatsOn != "GND_P" || in.Nets["D_P"].DiffPair != "D_N" {
		t.Errorf("input changed: %+v", in)
	}
}
