package app

import (
	"encoding/json"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/protocol"
)

// These are connector-wait budgets, not the HTTP round trip. Keep the current
// wire contract (daemon subtracts response grace) explicit at this boundary.
const (
	documentOpenReadBudget  = 30 * time.Second
	schematicPinsReadBudget = 150 * time.Second
)

// All CLI dispatch paths converge in postAction, including Apply and layout.
// A deliberately short diagnostic deadline remains short; legacy 90s pin reads
// get the same floor as reads that previously inherited the default 20s.
func effectiveReadTimeout(action string, payload any, requested time.Duration) time.Duration {
	if requested < defaultActionTimeout {
		return requested
	}
	budget := time.Duration(0)
	switch action {
	case "document.open", "schematic.page.open":
		budget = documentOpenReadBudget
	case "schematic.components.list":
		var flags struct {
			IncludePins bool `json:"includePins"`
		}
		data, err := json.Marshal(payload)
		if err == nil && json.Unmarshal(data, &flags) == nil && flags.IncludePins {
			budget = schematicPinsReadBudget
		}
	}
	if budget > 0 && requested < budget+protocol.DispatchResponseGrace {
		requested = budget + protocol.DispatchResponseGrace
	}
	return schematicIdentityReadTimeout(action, payload, requested)
}
