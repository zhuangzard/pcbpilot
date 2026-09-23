package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

// buildSchPlan emits the same version-1 playbook consumed by sch apply.
// Only explicit marker additions are supported. An existing NC may be cleared
// only as the first guarded stage of connecting that same pin.
func buildSchPlan(a, b connectivity.Document) (*playbook, error) {
	a, b = cloneSchPlanState(a), cloneSchPlanState(b)
	if err := a.Validate(); err != nil {
		return nil, err
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	if a.ProjectID == "" || a.DocumentID == "" || a.ProjectID != b.ProjectID || a.DocumentID != b.DocumentID {
		return nil, fmt.Errorf("matching projectId/documentId required; refresh page snapshots")
	}
	ac, bc := map[string]connectivity.Component{}, map[string]connectivity.Component{}
	for _, c := range a.Components {
		ac[c.ID] = c
	}
	for _, c := range b.Components {
		bc[c.ID] = c
	}
	// An explicit open pin becomes connected when its new marker edge is
	// added. Normalize only that declaration for comparison; all identity,
	// pin inventory and geometry changes remain unsupported. NC -> connected is
	// the only NC transition; validate the explicit edge kind before emitting it.
	oldEdges := map[[2]string]bool{}
	newEdges := map[[2]string]bool{}
	for _, edge := range a.Connections {
		oldEdges[[2]string{edge.ComponentID, edge.PinNumber}] = true
	}
	for _, edge := range b.Connections {
		newEdges[[2]string{edge.ComponentID, edge.PinNumber}] = true
	}
	clearNC := map[[2]string]bool{}
	for id, c := range ac {
		c.Pins = append([]connectivity.Pin(nil), c.Pins...)
		for i, pin := range c.Pins {
			key := [2]string{id, pin.Number}
			if pin.NoConnected && oldEdges[key] {
				return nil, fmt.Errorf("%s.%s baseline cannot be both connected and NC", id, pin.Number)
			}
			if pin.NoConnected && !oldEdges[key] && newEdges[key] {
				c.Pins[i].NoConnected = false
				clearNC[key] = true
			}
			if pin.ConnectionState == "unconnected" && !oldEdges[key] && newEdges[key] {
				c.Pins[i].ConnectionState = ""
			}
		}
		ac[id] = c
	}
	for id, c := range bc {
		for _, pin := range c.Pins {
			if pin.NoConnected && newEdges[[2]string{id, pin.Number}] {
				return nil, fmt.Errorf("%s.%s target cannot be both connected and NC", id, pin.Number)
			}
		}
	}
	if !reflect.DeepEqual(ac, bc) || !reflect.DeepEqual(a.Modules, b.Modules) {
		return nil, fmt.Errorf("component/module changes unsupported; NC may only be cleared with a new explicit marker connection on the same pin; no plan generated")
	}
	an, bn := map[string]connectivity.Net{}, map[string]connectivity.Net{}
	for _, n := range a.Nets {
		an[n.ID] = n
	}
	for _, n := range b.Nets {
		bn[n.ID] = n
	}
	for id, n := range an {
		if bn[id] != n {
			return nil, fmt.Errorf("net removal/rename/change unsupported: %s", id)
		}
	}
	key := func(c connectivity.Connection) [2]string { return [2]string{c.ComponentID, c.PinNumber} }
	old, next := map[[2]string]connectivity.Connection{}, map[[2]string]connectivity.Connection{}
	for _, c := range a.Connections {
		old[key(c)] = c
	}
	for _, c := range b.Connections {
		next[key(c)] = c
	}
	for k, c := range old {
		n, ok := next[k]
		if !ok || n.NetID != c.NetID {
			return nil, fmt.Errorf("disconnect/rewire unsupported: %v", k)
		}
	}
	additions := []connectivity.Connection{}
	for k, c := range next {
		if _, ok := old[k]; !ok {
			switch c.Kind {
			case "power", "ground", "net_port_in", "net_port_out", "net_port_bi":
			default:
				return nil, fmt.Errorf("%v: explicit power/ground/net_port kind required; wire routing is not implemented", k)
			}
			if bn[c.NetID].Name == "" {
				return nil, fmt.Errorf("net name required")
			}
			additions = append(additions, c)
		}
	}
	sort.Slice(additions, func(i, j int) bool {
		a, b := additions[i], additions[j]
		if a.ComponentID != b.ComponentID {
			return a.ComponentID < b.ComponentID
		}
		return a.PinNumber < b.PinNumber
	})
	zero := 0
	stop := false
	p := &playbook{Version: 1, RequireFullExecution: true, Meta: playbookMeta{Name: "Connectivity additions", Project: a.ProjectID, Doc: a.DocumentID}, Defaults: stepPolicy{Retry: &zero, ContinueOnError: &stop}, Steps: []playbookStep{}}
	state := a
	check := func() {
		copyState := cloneSchPlanState(state)
		p.Steps = append(p.Steps, playbookStep{ID: fmt.Sprintf("check-%03d", len(p.Steps)+1), Action: "schematic.read", Payload: map[string]any{"includeCheck": false}, ExpectedConnectivity: &copyState})
	}
	check()
	state.Nets = b.Nets
	for _, c := range additions {
		if clearNC[key(c)] {
			p.Steps = append(p.Steps, playbookStep{
				ID: fmt.Sprintf("clear-nc-%03d", len(p.Steps)+1), Action: "schematic.pin.set_no_connect",
				Payload: map[string]any{"designator": bc[c.ComponentID].Ref, "pins": []string{c.PinNumber}, "noConnected": false},
				Assert:  map[string]string{"$.notApplied": "len==0"},
			})
			setSchPlanPinState(&state, c.ComponentID, c.PinNumber, false, "unconnected")
			check()
		}
		p.Steps = append(p.Steps, playbookStep{ID: fmt.Sprintf("connect-%03d", len(p.Steps)+1), Run: "sch autoconnect", Flags: map[string]any{"pin": bc[c.ComponentID].Ref + ":" + c.PinNumber, "net": bn[c.NetID].Name, "kind": c.Kind, "strict": true, "offset-min": 10, "offset-max": 80, "offset-step": 5, "offset-cap": 300}})
		state.Connections = append(append([]connectivity.Connection(nil), state.Connections...), c)
		setSchPlanPinState(&state, c.ComponentID, c.PinNumber, false, "")
		check()
	}
	if len(additions) > 0 {
		p.Steps = append(p.Steps, playbookStep{ID: "save", Action: "schematic.save"})
		check()
	}
	return p, nil
}

// Copy every slice changed by state progression or Document.Validate. Emitted
// baseline/intermediate checkpoints must never share mutable pin or issue data.
func cloneSchPlanState(d connectivity.Document) connectivity.Document {
	d.Components = append([]connectivity.Component(nil), d.Components...)
	for i := range d.Components {
		d.Components[i].Pins = append([]connectivity.Pin(nil), d.Components[i].Pins...)
	}
	d.Connections = append([]connectivity.Connection(nil), d.Connections...)
	d.Nets = append([]connectivity.Net(nil), d.Nets...)
	d.Issues = append([]connectivity.Issue(nil), d.Issues...)
	return d
}

func setSchPlanPinState(d *connectivity.Document, component, number string, nc bool, connectionState string) {
	for i := range d.Components {
		if d.Components[i].ID != component {
			continue
		}
		for j := range d.Components[i].Pins {
			if d.Components[i].Pins[j].Number == number {
				d.Components[i].Pins[j].NoConnected = nc
				d.Components[i].Pins[j].ConnectionState = connectionState
			}
		}
	}
}

func newSchPlanCmd(stdout io.Writer) *cobra.Command {
	return &cobra.Command{Use: "plan <before.json> <after.json>", Short: "Generate a guarded sch apply playbook for explicit marker additions, including NC-to-connected pins (offline)", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		var docs [2]connectivity.Document
		for i, path := range args {
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if e = json.Unmarshal(raw, &docs[i]); e != nil {
				return e
			}
		}
		p, e := buildSchPlan(docs[0], docs[1])
		if e != nil {
			return e
		}
		return json.NewEncoder(stdout).Encode(p)
	}}
}
