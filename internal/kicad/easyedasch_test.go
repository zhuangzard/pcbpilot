package kicad

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A synthetic EasyEDA Pro export: a resistor symbol, a ground flag, a power
// flag and a net port; two pages.
var fixtureFiles = map[string]string{
	"project.json": `{"boards":{"B":{"schematic":"s1","pcb":"p1"}},
 "symbols":{"res":{"title":"RES 0603"},"gnd":{"title":"GND"},"pwr":{"title":"VCC"},"port":{"title":"Netport"}},
 "footprints":{"fp":{"title":"R0603"}},
 "schematics":{"s1":{"name":"S","sheets":[{"uuid":"a","id":1,"name":"MAIN"},{"uuid":"b","id":2,"name":"OUT"}]}}}`,
	"SYMBOL/res.esym": `["DOCTYPE","SYMBOL","1.1"]
["HEAD",{"originX":0,"originY":0,"version":"2","symbolType":2}]
["PART","RES.1",{"BBOX":[-10,-4,10,4]}]
["PIN","e3",1,null,20,0,10,180,null,0,0]
["ATTR","e0","e3","NAME","2",0,0,null,null,0,"st1",0]
["ATTR","e1","e3","NUMBER","2",0,0,null,null,0,"st2",0]
["ATTR","e2","e3","Pin Type","IN",0,0,null,null,0,"st2",0]
["PIN","e7",1,null,-20,0,10,0,null,0,0]
["ATTR","e4","e7","NAME","1",0,0,null,null,0,"st2",0]
["ATTR","e5","e7","NUMBER","1",0,0,null,null,0,"st1",0]
["ATTR","e6","e7","Pin Type","IN",0,0,null,null,0,"st2",0]
["RECT","e8",-10,4,10,-4,0,0,0,"st3",0]
["ATTR","e9","","Designator","R?",0,0,null,null,0,"st4",0]`,
	"SYMBOL/gnd.esym": `["DOCTYPE","SYMBOL","1.1"]
["HEAD",{"originX":0,"originY":0,"version":"2","symbolType":18}]
["PART","",{"BBOX":[-10.5,-19.5,10.5,-9.5]}]
["PIN","e15",1,null,0,0,10,270,null,0,0]
["ATTR","e1","e15","NUMBER","1",0,0,null,null,90,"st2",0]
["POLY","e3",[-10,-10,10,-10],false,"st4",0]
["ATTR","e7","","Symbol","Ground-GND",0,0,null,null,360,"st5",0]`,
	"SYMBOL/pwr.esym": `["DOCTYPE","SYMBOL","1.1"]
["HEAD",{"originX":0,"originY":0,"version":"2","symbolType":18}]
["PART","",{"BBOX":[-5.5,4.5,5.5,10.5]}]
["PIN","e11",1,null,0,0,5,90,null,0,0]
["ATTR","e1","e11","NUMBER","1",0,0,null,null,90,"st2",0]
["ATTR","e6","","Symbol","Power-5V",0,0,null,null,360,"st5",0]`,
	"SYMBOL/port.esym": `["DOCTYPE","SYMBOL","1.1"]
["HEAD",{"originX":0,"originY":0,"version":"2","symbolType":19}]
["PART","",{"BBOX":[9.5,-5.5,40.5,5.5]}]
["PIN","e11",1,null,0,0,10,0,null,0,0]
["ATTR","e1","e11","NUMBER","1",0,0,null,null,360,"st2",0]
["POLY","e3",[10,0,20,5,30,5,40,0,30,-5,20,-5,10,0],true,"st3",0]
["ATTR","e5","","Symbol","Netport-BI",0,0,null,null,360,"st4",0]`,
	// MAIN: R1 vertical (rot 90): pin 1 at (100,80) down to GND, pin 2 at
	// (100,120) up to VCC through a wire named VCC... and R2 horizontal at
	// (200,100): pin 1 (180,100) wired to R1's top wire by a T, pin 2 (220,100)
	// to a net port SIG.
	"SHEET/s1/1.esch": `["DOCTYPE","SCH","1.1"]
["COMPONENT","c1","RES.1",100,100,90,0,{},0]
["ATTR","a1","c1","Symbol","res",0,0,null,null,0,"st1",0]
["ATTR","a2","c1","Designator","R1",0,1,110,100,0,"st1",0]
["ATTR","a3","c1","Footprint","fp",0,0,null,null,0,"st1",0]
["ATTR","a4","c1","Supplier Part","C25804",0,0,null,null,0,"st1",0]
["ATTR","a5","c1","Name","={Manufacturer Part}",0,1,110,95,0,"st1",0]
["ATTR","a6","c1","Manufacturer Part","0603WAF1002T5E",0,0,null,null,0,"st1",0]
["ATTR","a6b","c1","Value","10k",0,1,110,95,0,"st1",0]
["COMPONENT","g1","",100,60,0,0,{},0]
["ATTR","a7","g1","Symbol","gnd",0,0,null,null,0,"st1",0]
["ATTR","a8","g1","Global Net Name","GND",0,1,100,40,0,"st1",0]
["COMPONENT","p1","",100,140,0,0,{},0]
["ATTR","a9","p1","Symbol","pwr",0,0,null,null,0,"st1",0]
["ATTR","a10","p1","Global Net Name","VCC",0,1,100,150,0,"st1",0]
["COMPONENT","c2","RES.1",200,100,0,0,{},0]
["ATTR","b1","c2","Symbol","res",0,0,null,null,0,"st1",0]
["ATTR","b2","c2","Designator","R2",0,1,200,110,0,"st1",0]
["ATTR","b3","c2","Value","1k",0,1,200,90,0,"st1",0]
["COMPONENT","n1","",240,100,0,0,{},0]
["ATTR","b4","n1","Symbol","port",0,0,null,null,0,"st1",0]
["ATTR","b5","n1","Name","SIG",0,1,285,100,0,"st1",0]
["WIRE","w1",[[100,80,100,60]],"st16",0]
["ATTR","n1a","w1","NET","GND",0,0,null,null,0,"st9",0]
["WIRE","w2",[[100,120,100,140]],"st16",0]
["ATTR","n2a","w2","NET","VCC",0,0,null,null,0,"st9",0]
["WIRE","w3",[[180,100,150,100],[150,100,150,130],[150,130,100,130]],"st16",0]
["ATTR","n3a","w3","NET","VCC",0,0,null,null,0,"st9",0]
["WIRE","w4",[[220,100,240,100]],"st16",0]
["ATTR","n4a","w4","NET","SIG",0,0,null,null,0,"st9",0]
["TEXT","t1",60,170,0,"MAIN BLOCK","st14"]
["RECT","r1",50,180,260,40,0,0,0,"st13",0]`,
	// OUT: R3 pin 1 on a wire named SIG (no port: SIG lives on MAIN → global
	// label), pin 2 no-connect; R4 pin 1 on a wire named LOCAL with a
	// stub, pin 2 on another LOCAL wire (same page → local labels).
	"SHEET/s1/2.esch": `["DOCTYPE","SCH","1.1"]
["COMPONENT","c3","RES.1",100,100,0,0,{},0]
["ATTR","d1","c3","Symbol","res",0,0,null,null,0,"st1",0]
["ATTR","d2","c3","Designator","R3",0,1,100,110,0,"st1",0]
["COMPONENT","c4","RES.1",100,200,0,0,{},0]
["ATTR","d3","c4","Symbol","res",0,0,null,null,0,"st1",0]
["ATTR","d4","c4","Designator","R4",0,1,100,210,0,"st1",0]
["WIRE","x1",[[80,100,60,100]],"st16",0]
["ATTR","x1a","x1","NET","SIG",0,0,null,null,0,"st9",0]
["WIRE","x2",[[80,200,60,200]],"st16",0]
["ATTR","x2a","x2","NET","LOCAL",0,0,null,null,0,"st9",0]
["WIRE","x3",[[120,200,140,200]],"st16",0]
["ATTR","x3a","x3","NET","LOCAL",0,0,null,null,0,"st9",0]
["ATTR","nc1","c3e3","NO_CONNECT","yes",0,0,120,100,0,"st6",0]`,
}

func writeFixtureEpro(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture.epro")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range fixtureFiles {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func TestConvertEasyedaSchematic(t *testing.T) {
	out := t.TempDir()
	res, err := ConvertEasyedaSchematic(writeFixtureEpro(t), EasyedaSchOptions{OutDir: out, Name: "fx"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) > 0 {
		t.Errorf("warnings: %v", res.Warnings)
	}
	if res.Parts != 4 || res.Power != 2 || res.Ports != 1 || res.NoConnects != 1 || len(res.Sheets) != 2 {
		t.Errorf("counts: %+v", res)
	}
	// EasyEDA reference connectivity: the T-joined wire puts R2.1 on VCC.
	want := map[string]string{"R1.1": "GND", "R1.2": "VCC", "R2.1": "VCC", "R2.2": "SIG", "R3.1": "SIG", "R4.1": "LOCAL", "R4.2": "LOCAL"}
	for k, v := range want {
		if res.PinNets[k] != v {
			t.Errorf("EasyEDA %s = %q, want %q", k, res.PinNets[k], v)
		}
	}
	if !strings.HasPrefix(res.PinNets["R3.2"], "unconnected-") {
		t.Errorf("R3.2 = %q", res.PinNets["R3.2"])
	}
	main, _ := os.ReadFile(filepath.Join(out, "fx_MAIN.kicad_sch"))
	other, _ := os.ReadFile(filepath.Join(out, "fx_OUT.kicad_sch"))
	for _, s := range []string{`(property "LCSC" "C25804"`, `(property "Value" "10k"`, `(property "Footprint" "R0603"`, `(global_label "SIG"`, `(lib_id "pcbpilot_power:GND")`, `(junction`, `(text "MAIN BLOCK"`} {
		if !strings.Contains(string(main), s) {
			t.Errorf("MAIN sheet lacks %s", s)
		}
	}
	for _, s := range []string{`(global_label "SIG"`, `(label "LOCAL"`, `(no_connect`} {
		if !strings.Contains(string(other), s) {
			t.Errorf("OUT sheet lacks %s", s)
		}
	}
	for _, f := range []string{"fx.kicad_sch", "fx.kicad_pro", "easyeda.kicad_sym", "pcbpilot_power.kicad_sym", "sym-lib-table"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}
	if _, err := KicadCLI(); err != nil {
		t.Skip(err)
	}
	nl, err := ExportSchNetlist(res.Root)
	if err != nil {
		t.Fatal(err)
	}
	cmp := ComparePinNets(res.PinNets, nl.PinNets(), func(n string) string {
		if strings.HasPrefix(n, "/") {
			return n[strings.LastIndex(n, "/")+1:]
		}
		return n
	})
	if !cmp.Equal {
		t.Fatalf("KiCad netlist differs from the EasyEDA connectivity: %+v\nkicad: %v", cmp, nl.PinNets())
	}
	if cmp.NamesEqual != cmp.NetsA-1 { // only the R3.2 no-connect is named differently
		t.Errorf("names: %+v", cmp)
	}
}

func TestConvertEasyedaSchematicNeedsBoard(t *testing.T) {
	p := writeFixtureEpro(t)
	if _, err := ConvertEasyedaSchematic(p, EasyedaSchOptions{OutDir: t.TempDir(), Board: "nope"}); err == nil {
		t.Fatal("unknown board accepted")
	}
}

func TestComparePinNets(t *testing.T) {
	a := map[string]string{"R1.1": "A", "R2.1": "A", "R1.2": "B"}
	b := map[string]string{"R1.1": "/s/A", "R2.1": "/s/A", "R1.2": "X"}
	c := ComparePinNets(a, b, func(s string) string { return strings.TrimPrefix(s, "/s/") })
	if !c.Equal || c.NamesEqual != 1 || len(c.RenamedNets) != 1 {
		t.Fatalf("%+v", c)
	}
	b["R2.1"] = "X"
	if c := ComparePinNets(a, b, nil); c.Equal || len(c.Mismatched) != 2 {
		t.Fatalf("%+v", c)
	}
}
