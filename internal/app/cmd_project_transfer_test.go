package app

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectArchiveValidationAndNoOverwrite(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("project.epru")
	w.Write([]byte("native fixture"))
	z.Close()
	value := map[string]any{"uuid": "project-1", "format": "epro2", "size": float64(b.Len()), "base64": base64.StdEncoding.EncodeToString(b.Bytes())}
	out := filepath.Join(t.TempDir(), "nested", "test.epro2")
	report, err := writeProjectArchive(value, out)
	if err != nil {
		t.Fatal(err)
	}
	if report["restoreVerified"] != false || report["zipIntegrityVerified"] != true {
		t.Fatal(report)
	}
	if _, err = writeProjectArchive(value, out); err == nil {
		t.Fatal("overwrote existing archive")
	}
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, b.Bytes()) {
		t.Fatal("archive bytes changed")
	}
	value["size"] = float64(1)
	if _, err = writeProjectArchive(value, out+"2"); err == nil {
		t.Fatal("accepted inconsistent size")
	}
	value["size"] = float64(3)
	value["base64"] = base64.StdEncoding.EncodeToString([]byte("bad"))
	if _, err = writeProjectArchive(value, out+"3"); err == nil {
		t.Fatal("accepted non-ZIP")
	}
}

func TestProjectTransferOfficialAdapters(t *testing.T) {
	for _, scenario := range []string{"open", "open-page-delayed", "open-page-missing", "open-page-wrong-project", "open-false", "open-wrong-project", "export", "export-wrong-project", "export-drift", "export-empty"} {
		t.Run(scenario, func(t *testing.T) {
			operation := "export"
			if strings.HasPrefix(scenario, "open") {
				operation = "open"
			}
			page := ""
			if strings.HasPrefix(scenario, "open-page") {
				page = "page-1"
			}
			code := projectTransferCode(operation, "target", page)
			// Exercise the generated browser script against contract mocks, including
			// context changes while an asynchronous export is in progress.
			fixture := `let checks=0, exports=0, pageReads=0, pageOpens=0; const scenario=` + strconvQuote(scenario) + `;
const eda={ dmt_Project:{
 openProject:async(id)=>{if(id!=="target")throw Error("bad target");return scenario!=="open-false"},
 getCurrentProjectInfo:async()=>({uuid:scenario.includes("wrong-project")||(scenario==="export-drift"&&checks++>0)?"other":"target"})
},dmt_Schematic:{getAllSchematicPagesInfo:async()=>{pageReads++;return scenario==="open-page-missing"||pageReads===1?[]:[{uuid:"page-1"}]}},
 dmt_EditorControl:{openDocument:async(id)=>{pageOpens++;if(id!=="page-1")throw Error("wrong page");return "tab"}},
 dmt_SelectControl:{getCurrentDocumentInfo:async()=>({uuid:"page-1",parentProjectUuid:"target"})},
 sys_FileManager:{getProjectFile:async(name,unused,format)=>{exports++;if(format!=="epro2"||unused!==undefined)throw Error("bad format");return new Blob([scenario==="export-empty"?"":"PKfixture"])}}};
const setTimeout=(f)=>{f();};
(async()=>{try{const value=await(async()=>{` + code + `})();console.log(JSON.stringify({ok:true,value,exports,pageReads,pageOpens}));}catch(e){console.log(JSON.stringify({ok:false,error:String(e),exports,pageReads,pageOpens}));}})();`
			cmd := exec.Command("node")
			cmd.Stdin = strings.NewReader(fixture)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			var got map[string]any
			if err = json.Unmarshal(out, &got); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			want := scenario == "open" || scenario == "export" || scenario == "open-page-delayed"
			if got["ok"] != want {
				t.Fatalf("%s", out)
			}
			if scenario == "open-page-delayed" && (got["pageOpens"] != float64(1) || got["pageReads"] != float64(2)) {
				t.Fatalf("%s", out)
			}
			if scenario == "open-page-missing" && got["pageOpens"] != float64(0) {
				t.Fatal("opened missing page")
			}
			if scenario == "export-wrong-project" && got["exports"] != float64(0) {
				t.Fatal("exported wrong project")
			}
		})
	}
}
func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestProjectTransferCLIRejectsUnsafeOrAmbiguousArguments(t *testing.T) {
	for _, args := range [][]string{
		{"project", "open", "--project-uuid", "p", "--window", "w"},
		{"project", "open", "--project-uuid", "p", "--uuid", "d", "--allow-discard-unsaved", "--window", "w"},
		{"project", "export", "--project-uuid", "p", "--window", "w", "--out", "bad.zip"},
		{"project", "export", "--project-uuid", "p", "--out", filepath.Join(t.TempDir(), "a.epro2")},
	} {
		var out, err bytes.Buffer
		if Run(args, &out, &err) == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
}
