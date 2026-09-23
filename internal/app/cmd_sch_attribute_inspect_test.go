package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestSchAttributeInspectCLIHelp(t *testing.T) {
	var out bytes.Buffer
	sch := newSchCmd(nil, &out, &out)
	sch.SetOut(&out)
	sch.SetErr(&out)
	sch.SetArgs([]string{"attribute-inspect", "--help"})
	if err := sch.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"--id", "get(id).toAsync().reset()", "undefined is unreadable", "--doc"} {
		if !strings.Contains(out.String(), phrase) {
			t.Fatalf("help missing %q: %s", phrase, out.String())
		}
	}
}

func TestSchAttributeInspectRequiresID(t *testing.T) {
	var out bytes.Buffer
	sch := newSchCmd(nil, &out, &out)
	sch.SetOut(&out)
	sch.SetErr(&out)
	sch.SetArgs([]string{"attribute-inspect"})
	if err := sch.Execute(); err == nil || !strings.Contains(err.Error(), "--id is required") {
		t.Fatalf("expected local --id validation, got %v", err)
	}
}
