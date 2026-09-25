package app

// Net labels (2026-09-24, user convention from Altium-style practice): inside
// one sheet, signals between modules are joined by NAME with a small net
// label; only nets that really cross sheets need a (large) net port. The
// "net_label" policy behaves exactly like "module_port" everywhere (routing,
// naming, ownership gates) except the marker kind it draws.

// libPortPolicy: a signal named at a module boundary (port or net label).
func libPortPolicy(policy string) bool { return policy == "module_port" || policy == "net_label" }

// libPortMarkerKind is the marker drawn for a policy.
func libPortMarkerKind(policy string) string {
	switch policy {
	case "local_ground":
		return "ground"
	case "local_power":
		return "power"
	case "net_label":
		return "net_label"
	}
	return "net_port_bi"
}
