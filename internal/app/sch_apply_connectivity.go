package app

import (
	"fmt"
	"github.com/zhuangzard/pcbpilot/internal/connectivity"
	"reflect"
)

func compareObserved(expected connectivity.Document, result map[string]any) error {
	live, err := connectivity.FromRead(result)
	if err != nil {
		return err
	}
	a, err := expected.NamedPins()
	if err != nil {
		return err
	}
	b, err := live.NamedPins()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(a, b) {
		return fmt.Errorf("connectivity mismatch: live pin-to-net differs from expected snapshot; stop and re-plan")
	}
	observedPins := map[[2]string]connectivity.Pin{}
	for _, c := range live.Components {
		for _, p := range c.Pins {
			observedPins[[2]string{c.Ref, p.Number}] = p
		}
	}
	for _, c := range expected.Components {
		for _, p := range c.Pins {
			got := observedPins[[2]string{c.Ref, p.Number}]
			if got.NoConnected != p.NoConnected {
				return fmt.Errorf("connectivity mismatch: %s.%s NC differs from expected snapshot; stop and re-plan", c.Ref, p.Number)
			}
			if p.ConnectionState == "unconnected" && got.ConnectionState != "unconnected" {
				return fmt.Errorf("connectivity mismatch: %s.%s lacks explicit empty-net/noConnected:false evidence; stop and re-plan", c.Ref, p.Number)
			}
		}
	}
	return nil
}
func (r *applyRunner) checkConnectivity(d *connectivity.Document) (any, error) {
	cfg := *r.cfg
	cfg.doc = d.DocumentID
	cfg.project = d.ProjectID
	res, err := requestAction(staleReadOptIn(&cfg, "sch apply connectivity checkpoint"), "schematic.read", r.window, map[string]any{"includeCheck": false})
	if err != nil {
		return nil, err
	}
	if res.Context == nil || res.Context.DocumentUUID != d.DocumentID || res.Context.ProjectUUID != d.ProjectID {
		return nil, fmt.Errorf("connectivity checkpoint target mismatch")
	}
	if err = compareObserved(*d, res.Result); err != nil {
		return nil, err
	}
	return map[string]any{"matched": true}, nil
}
