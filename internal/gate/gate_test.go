package gate

import "testing"

func TestStatusOKOnlyPass(t *testing.T) {
	for _, s := range []Status{Fail, CannotCheck, Warn, Skip, "", "PASS ", "bogus"} {
		if s.OK() {
			t.Errorf("%q.OK() = true", s)
		}
	}
	if !Pass.OK() {
		t.Error("Pass.OK() = false")
	}
}

func TestParse(t *testing.T) {
	cases := map[string]Status{"pass": Pass, " FAIL ": Fail, "cannot-check": CannotCheck, "warn": Warn,
		"skip": Skip, "": CannotCheck, "ok": CannotCheck, "passed": CannotCheck}
	for in, want := range cases {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAggregateTruthTable(t *testing.T) {
	req := func(s Status) Result { return Result{Name: "r", Status: s} }
	adv := func(s Status) Result { return Result{Name: "a", Status: s, Advisory: true} }
	cases := []struct {
		name string
		in   []Result
		want Status
	}{
		{"none", nil, CannotCheck},
		{"all pass", []Result{req(Pass), req(Pass)}, Pass},
		{"one fail", []Result{req(Pass), req(Fail)}, Fail},
		{"fail beats cannot-check", []Result{req(CannotCheck), req(Fail)}, Fail},
		{"cannot-check", []Result{req(Pass), req(CannotCheck)}, CannotCheck},
		{"advisory cannot-check still cannot-check", []Result{req(Pass), adv(CannotCheck)}, CannotCheck},
		{"advisory fail still fail", []Result{req(Pass), adv(Fail)}, Fail},
		{"required warn fails", []Result{req(Pass), req(Warn)}, Fail},
		{"advisory warn passes", []Result{req(Pass), adv(Warn)}, Pass},
		{"required skip never pass", []Result{req(Pass), req(Skip)}, Skip},
		{"advisory skip passes", []Result{req(Pass), adv(Skip)}, Pass},
		{"cannot-check beats skip", []Result{req(Skip), req(CannotCheck)}, CannotCheck},
		{"unknown status", []Result{req(Pass), req("maybe")}, CannotCheck},
	}
	for _, c := range cases {
		got := Aggregate(c.in...)
		if got != c.want {
			t.Errorf("%s: Aggregate = %q, want %q", c.name, got, c.want)
		}
		if c.want != Pass && got.OK() {
			t.Errorf("%s: OK() true for %q", c.name, got)
		}
	}
}

func TestExitCode(t *testing.T) {
	cases := map[Status]int{Pass: 0, Fail: 6, CannotCheck: 5, Skip: 5, Warn: 5, "x": 5}
	for s, want := range cases {
		if got := s.ExitCode(); got != want {
			t.Errorf("%q.ExitCode() = %d, want %d", s, got, want)
		}
	}
	if FromBool(true) != Pass || FromBool(false) != Fail {
		t.Error("FromBool")
	}
}
