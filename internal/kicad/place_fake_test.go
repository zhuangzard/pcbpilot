package kicad

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fakePlaceEnv makes the test binary stand in for KiCad's python: Place
// runs PCBPILOT_KICAD_PYTHON=<test binary>, which prints this variable's
// value as place.py's stdout and exits with PCBPILOT_FAKE_PLACE_EXIT.
const fakePlaceEnv = "PCBPILOT_FAKE_PLACE_STDOUT"

func TestMain(m *testing.M) {
	if out, ok := os.LookupEnv(fakePlaceEnv); ok {
		fmt.Print(out)
		if os.Getenv("PCBPILOT_FAKE_PLACE_EXIT") == "1" {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakePlace(t *testing.T, stdout string, fail bool) (*PlaceResult, error) {
	t.Helper()
	t.Setenv(EnvPython, os.Args[0])
	t.Setenv(fakePlaceEnv, stdout)
	t.Setenv("PCBPILOT_FAKE_PLACE_EXIT", map[bool]string{true: "1", false: "0"}[fail])
	return Place("in.kicad_pcb", map[string]Pose{"H1": {}}, "out.kicad_pcb")
}

func TestPlaceParsesRejection(t *testing.T) {
	_, err := fakePlace(t, "swig noise\nPCBPILOT_RESULT {\"ok\": false, \"error\": \"place rejected, board not written: H1 is on 2 footprints\", "+
		"\"missing\": [\"Q9\"], \"ambiguous\": {\"H1\": [\"u-a\", \"u-b\"]}}\n", true)
	var pe *PlaceError
	if !errors.As(err, &pe) || pe.Missing[0] != "Q9" || len(pe.Ambiguous["H1"]) != 2 || !strings.Contains(err.Error(), "H1 is on 2 footprints") {
		t.Fatalf("rejection: %#v", err)
	}
}

func TestPlaceParsesSuccessAndCrash(t *testing.T) {
	res, err := fakePlace(t, "noise PCBPILOT_RESULT \nPCBPILOT_RESULT {\"ok\": true, \"moved\": 3, \"flipped\": 1, \"removedTracks\": 2, \"missing\": [], \"locked\": [\"J1\"]}\n", false)
	if err != nil || res.Moved != 3 || res.Flipped != 1 || res.Locked[0] != "J1" {
		t.Fatalf("success: %+v %v", res, err)
	}
	// A crash with no result line is an error, not a PlaceError.
	_, err = fakePlace(t, "Traceback ...\n", true)
	var pe *PlaceError
	if err == nil || errors.As(err, &pe) {
		t.Fatalf("crash: %v", err)
	}
}
