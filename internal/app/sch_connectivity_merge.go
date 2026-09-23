package app

import (
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

// A project-wide snapshot has no single active-page identity. Page IDs remain
// on the components; leaving DocumentID empty prevents accidental single-page
// Apply of a multi-page electrical inventory.
func mergeSchConnectivityPages(pages []connectivity.Document) (connectivity.Document, error) {
	out := connectivity.Document{SchemaVersion: "1.4", Components: []connectivity.Component{}, Nets: []connectivity.Net{}, Connections: []connectivity.Connection{}}
	if len(pages) == 0 {
		return out, fmt.Errorf("no schematic pages found")
	}
	nets := map[string]connectivity.Net{}
	modules := map[string]connectivity.Module{}
	pageIDs := map[string]bool{}
	for _, page := range pages {
		if page.ProjectID == "" || page.DocumentID == "" {
			return out, fmt.Errorf("page snapshot requires projectId and documentId")
		}
		if out.ProjectID != "" && out.ProjectID != page.ProjectID {
			return out, fmt.Errorf("cannot merge snapshots from different projects")
		}
		if pageIDs[page.DocumentID] {
			return out, fmt.Errorf("duplicate page snapshot %s", page.DocumentID)
		}
		pageIDs[page.DocumentID] = true
		out.ProjectID = page.ProjectID
		if err := page.Validate(); err != nil {
			return out, fmt.Errorf("page %s: %w", page.DocumentID, err)
		}
		for _, c := range page.Components {
			if c.PageID != "" && c.PageID != page.DocumentID {
				return out, fmt.Errorf("component %s has conflicting page identity", c.Ref)
			}
			c.PageID = page.DocumentID
			out.Components = append(out.Components, c)
		}
		for _, n := range page.Nets {
			if previous, ok := nets[n.ID]; ok && previous != n {
				return out, fmt.Errorf("net %s has conflicting definitions across pages", n.ID)
			}
			nets[n.ID] = n
		}
		for _, m := range page.Modules {
			if m.ID == "" {
				return out, fmt.Errorf("page %s has a module without an id", page.DocumentID)
			}
			if previous, ok := modules[m.ID]; ok {
				if !reflect.DeepEqual(previous, m) {
					return out, fmt.Errorf("module %s has conflicting definitions across pages", m.ID)
				}
				continue
			}
			modules[m.ID] = m
			out.Modules = append(out.Modules, m)
		}
		out.Connections = append(out.Connections, page.Connections...)
		out.Issues = append(out.Issues, page.Issues...)
	}
	for _, n := range nets {
		out.Nets = append(out.Nets, n)
	}
	if len(pages) == 1 {
		out.DocumentID = pages[0].DocumentID
	}
	sort.Slice(out.Components, func(i, j int) bool { return out.Components[i].ID < out.Components[j].ID })
	sort.Slice(out.Nets, func(i, j int) bool { return out.Nets[i].ID < out.Nets[j].ID })
	sort.Slice(out.Connections, func(i, j int) bool {
		a, b := out.Connections[i], out.Connections[j]
		if a.ComponentID != b.ComponentID {
			return a.ComponentID < b.ComponentID
		}
		return a.PinNumber < b.PinNumber
	})
	if err := out.Validate(); err != nil {
		return out, err
	}
	// Validate appends unconnected-pin findings. Each page has already been
	// validated, so retain each exact issue once rather than tripling warnings.
	seenIssues := map[connectivity.Issue]bool{}
	issues := out.Issues
	out.Issues = nil
	for _, issue := range issues {
		if !seenIssues[issue] {
			seenIssues[issue] = true
			out.Issues = append(out.Issues, issue)
		}
	}
	return out, nil
}

func connectivityPageContext(result *actionResult, projectID, pageID string) error {
	if result == nil || result.Context == nil || result.Context.ProjectUUID != projectID || result.Context.DocumentUUID != pageID || result.Context.DocumentType != "schematic" {
		return fmt.Errorf("connectivity snapshot target mismatch: expected project %s page %s", projectID, pageID)
	}
	return nil
}

func collectSchConnectivityAllPages(cfg *appConfig, window string) (out connectivity.Document, err error) {
	// Per-page reads explicitly choose their own document. A caller's --doc
	// must not silently steer the subsequent pages back to the starting page.
	readCfg := *cfg
	readCfg.doc = ""
	docs, active, win, err := discoverDocs(&readCfg, window)
	if err != nil {
		return out, err
	}
	current, err := requestAction(&readCfg, "document.current", win, nil)
	if err != nil {
		return out, err
	}
	if active == "" || current.Context == nil || current.Context.ProjectUUID == "" || current.Context.DocumentUUID != active {
		return out, fmt.Errorf("cannot establish initial project/document identity for all-pages connectivity")
	}
	projectID := current.Context.ProjectUUID
	changedPage := false
	defer func() {
		if changedPage {
			scope := pageScope{window: win, prevActive: active, switched: true}
			if restoreErr := scope.restore(&readCfg); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore original document %s: %w", active, restoreErr))
			}
		}
	}()
	var pages []connectivity.Document
	for _, doc := range docs {
		if doc.Type != "schematic" {
			continue
		}
		// Mark before switching: an unsuccessful open may still have changed
		// the foreground document and must also trigger restoration.
		changedPage = changedPage || doc.UUID != active
		scope, switchErr := switchToPage(&readCfg, win, doc.UUID)
		if switchErr != nil {
			return out, switchErr
		}
		if !scope.settled {
			return out, fmt.Errorf("page %s did not settle; no connectivity snapshot emitted", doc.Name)
		}
		raw, readErr := requestAction(&readCfg, "schematic.read", win, map[string]any{"includeCheck": false})
		if readErr != nil {
			return out, readErr
		}
		if err := connectivityPageContext(raw, projectID, doc.UUID); err != nil {
			return out, err
		}
		if raw.Result == nil {
			return out, fmt.Errorf("page %s: missing semantic snapshot", doc.Name)
		}
		pinRaw, pinErr := requestAction(&readCfg, "schematic.components.list", win, map[string]any{"includePins": true, "includeBBox": true, "includeDeviceIdentity": true})
		if pinErr != nil {
			return out, pinErr
		}
		if err := connectivityPageContext(pinRaw, projectID, doc.UUID); err != nil {
			return out, err
		}
		parts, ok := pinRaw.Result["components"]
		if !ok {
			return out, fmt.Errorf("page %s: missing pin inventory", doc.Name)
		}
		raw.Result["components"] = parts
		part, convertErr := connectivity.FromRead(raw.Result)
		if convertErr != nil {
			return out, fmt.Errorf("page %s: %w", doc.Name, convertErr)
		}
		part.ProjectID, part.DocumentID = projectID, doc.UUID
		for i := range part.Components {
			part.Components[i].PageID, part.Components[i].PageName = doc.UUID, doc.Name
		}
		pages = append(pages, part)
	}
	return mergeSchConnectivityPages(pages)
}
