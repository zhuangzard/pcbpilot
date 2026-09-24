package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

// schLayoutCompositionDevice is one library identity row. It accepts the
// part-selection shape (uuid) and the connectivity shape (deviceUuid).
type schLayoutCompositionDevice struct {
	ID          string `json:"id"`
	LibraryUUID string `json:"libraryUuid"`
	UUID        string `json:"uuid"`
	DeviceUUID  string `json:"deviceUuid"`
	Name        string `json:"name"`
	MPN         string `json:"mpn"`
}

type schLayoutCompositionInput struct {
	Source     SchematicZonesInput
	Page       SchematicRenderInput
	Devices    []schLayoutCompositionDevice
	ProjectID  string
	DocumentID string
	TitleBlock map[string]string
}

func newSchLayoutCompositionCmd(stdout io.Writer) *cobra.Command {
	var source, page, devices, project, document, titleBlock, out string
	c := &cobra.Command{Use: "layout-composition", Short: "Convert a zones source plus one selected layout-sheet-plan page into compose input (offline)", Long: `Builds the schemaVersion:1 composition consumed by
sch compose --layout-page, so a solved zones page is applied without hand-editing JSON.

--source   the layout-plan --zones input (measured components, pins, nets, NC states)
--page     one pages[] object from layout-sheet-plan (selected geometry, no variants)
--devices  JSON array of {id, libraryUuid, uuid|deviceUuid, name?|mpn?} for every
           component on the page (the real library identity; never guessed)
--project/--document  the target project UUID and schematic PAGE UUID

The connectivity 1.4 IR covers exactly the page's components: pins, names and
nets come from the source measurements, NC/unconnected from the page's selected
pinStates, net roles from netPolicies (local_power -> power, local_ground ->
ground, as the zone solver used them). Modules copy the page zones' local placements/wires/flags verbatim;
sheet, border and keepouts copy the page's paper evidence. Any disagreement
between source and page (designator, pin name, pin net, member set) is refused.
Nets that also appear on other pages keep their names; cross-page continuity is
carried by the module_port / power / ground markers already in the geometry.
No editor calls are made.`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if source == "" || page == "" || devices == "" || project == "" || document == "" || out == "" {
			return fmt.Errorf("--source, --page, --devices, --project, --document and --out are required")
		}
		if err := validateSchCompositionOutputPaths([]string{source, page, devices, titleBlock}, []string{out}); err != nil {
			return err
		}
		var in schLayoutCompositionInput
		in.ProjectID, in.DocumentID = project, document
		raw, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if in.Source, err = decodeSchematicZonesInput(raw); err != nil {
			return err
		}
		if raw, err = os.ReadFile(page); err != nil {
			return err
		}
		p, err := decodeSchCompositionLayoutPage(raw)
		if err != nil {
			return err
		}
		in.Page = *p
		if raw, err = os.ReadFile(devices); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &in.Devices); err != nil {
			return fmt.Errorf("--devices: %w", err)
		}
		if titleBlock != "" {
			if raw, err = os.ReadFile(titleBlock); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &in.TitleBlock); err != nil {
				return fmt.Errorf("--title-block: %w", err)
			}
		}
		comp, err := buildSchLayoutComposition(in)
		if err != nil {
			return err
		}
		b, err := json.MarshalIndent(comp, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(out, append(b, '\n'), 0644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "layout-composition: %d modules, %d components, %d nets -> %s\n", len(comp.Modules), len(comp.Connectivity.Components), len(comp.Connectivity.Nets), out)
		return nil
	}}
	c.Flags().StringVar(&source, "source", "", "layout-plan --zones source JSON")
	c.Flags().StringVar(&page, "page", "", "one selected page object from layout-sheet-plan pages[]")
	c.Flags().StringVar(&devices, "devices", "", "device identity JSON array {id, libraryUuid, uuid|deviceUuid}")
	c.Flags().StringVar(&project, "project", "", "target project UUID")
	c.Flags().StringVar(&document, "document", "", "target schematic page UUID")
	c.Flags().StringVar(&titleBlock, "title-block", "", "optional per-page title-block text map JSON")
	c.Flags().StringVar(&out, "out", "", "write composition JSON")
	return c
}

func buildSchLayoutComposition(in schLayoutCompositionInput) (*schCompositionSource, error) {
	page := in.Page
	if page.Sheet == nil || len(page.Zones) == 0 {
		return nil, fmt.Errorf("page requires sheet evidence and zones")
	}
	sources := map[string]SchematicLayoutComponent{}
	for _, c := range in.Source.Components {
		sources[c.ID] = c
	}
	zoneMembers := map[string][]string{}
	for _, z := range in.Source.Zones {
		zoneMembers[z.ID] = z.ComponentIDs
	}
	devices := map[string]schLayoutCompositionDevice{}
	for _, d := range in.Devices {
		devices[d.ID] = d
	}
	doc := connectivity.Document{SchemaVersion: "1.4", ProjectID: in.ProjectID, DocumentID: in.DocumentID, Components: []connectivity.Component{}, Nets: []connectivity.Net{}, Connections: []connectivity.Connection{}}
	out := &schCompositionSource{SchemaVersion: 1, Sheet: page.Sheet.Bounds, Keepouts: append([]layoutBBox{}, page.Sheet.Keepouts...), TitleBlock: in.TitleBlock}
	border := page.Sheet.Border
	out.SheetBorder = &border
	nets := map[string]bool{}
	for _, z := range page.Zones {
		if z.Layout == nil {
			return nil, fmt.Errorf("zone %s has no selected layout", z.ID)
		}
		members, ok := zoneMembers[z.ID]
		if !ok {
			return nil, fmt.Errorf("zone %s is not in the source", z.ID)
		}
		if len(members) != len(z.Layout.Placements) {
			return nil, fmt.Errorf("zone %s member count differs between source and page", z.ID)
		}
		placed := map[string]powerLayoutPlacement{}
		for _, p := range z.Layout.Placements {
			placed[z.Layout.ComponentIDs[p.Designator]] = p
		}
		mod := connectivity.Module{ID: z.ID, Name: z.Title}
		for _, id := range members {
			src, ok := sources[id]
			if !ok {
				return nil, fmt.Errorf("zone %s member %s has no source measurement", z.ID, id)
			}
			p, ok := placed[id]
			if !ok || p.Designator != src.Measurement.Designator {
				return nil, fmt.Errorf("zone %s member %s designator differs between source and page", z.ID, id)
			}
			dev, ok := devices[id]
			uuid := dev.DeviceUUID
			if uuid == "" {
				uuid = dev.UUID
			}
			if !ok || dev.LibraryUUID == "" || !isDeviceLibraryUUID(uuid) {
				return nil, fmt.Errorf("%s (%s) has no real library/device UUID in --devices", id, p.Designator)
			}
			name := dev.Name
			if name == "" {
				name = dev.MPN
			}
			pagePins := map[string]powerLayoutPin{}
			for _, q := range p.Pins {
				pagePins[q.Number] = q
			}
			if len(pagePins) != len(src.Measurement.Pins) {
				return nil, fmt.Errorf("%s pin set differs between source and page", p.Designator)
			}
			comp := connectivity.Component{ID: id, Ref: p.Designator, Device: connectivity.Device{LibraryUUID: dev.LibraryUUID, UUID: uuid, Name: name}}
			for _, q := range src.Measurement.Pins {
				pq, ok := pagePins[q.Number]
				if !ok || pq.Name != q.Name || pq.Net != q.Net {
					return nil, fmt.Errorf("%s.%s name/net differs between source and page", p.Designator, q.Number)
				}
				pin := connectivity.Pin{Number: q.Number, Name: q.Name}
				switch z.Layout.PinStates[id][q.Number] {
				case "nc":
					pin.NoConnected = true
				case "unconnected":
					pin.ConnectionState = "unconnected"
				case "":
				default:
					return nil, fmt.Errorf("%s.%s has unknown pin state %q", p.Designator, q.Number, z.Layout.PinStates[id][q.Number])
				}
				comp.Pins = append(comp.Pins, pin)
				if q.Net != "" {
					nets[q.Net] = true
					doc.Connections = append(doc.Connections, connectivity.Connection{ComponentID: id, PinNumber: q.Number, NetID: "net:" + q.Net, Kind: "pin_net"})
				}
			}
			doc.Components = append(doc.Components, comp)
			if id == z.CoreComponentID {
				mod.CoreComponents = append(mod.CoreComponents, id)
			} else {
				mod.PeripheralComponents = append(mod.PeripheralComponents, id)
			}
		}
		if len(mod.CoreComponents) != 1 {
			return nil, fmt.Errorf("zone %s core %s is not a member", z.ID, z.CoreComponentID)
		}
		doc.Modules = append(doc.Modules, mod)
		out.Modules = append(out.Modules, schCompositionModule{ID: z.ID, Title: z.Title, Placements: z.Layout.Placements, Wires: z.Layout.Wires, Flags: z.Layout.Flags})
	}
	names := make([]string, 0, len(nets))
	for n := range nets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		// Same role mapping the zone solver used for its peripheral-direct gate.
		net := connectivity.Net{ID: "net:" + n, Name: n}
		switch in.Source.NetPolicies[n] {
		case "local_power":
			net.Role = "power"
		case "local_ground":
			net.Role = "ground"
		}
		doc.Nets = append(doc.Nets, net)
	}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	out.Connectivity = doc
	return out, nil
}
