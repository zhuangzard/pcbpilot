package app

import "testing"

func TestSchComposeClearAcceptsV4PinAttributes(t *testing.T) {
	page := map[string]any{
		"components": []any{map[string]any{"primitiveId": "bf0e4d6c28081e0d"}},
		"attributes": []any{
			map[string]any{"primitiveId": "a1", "ParentPrimitiveId": "bf0e4d6c28081e0d"},
			map[string]any{"primitiveId": "a2", "ParentPrimitiveId": "bf0e4d6c28081e0d-e9"},
		},
		"objects": []any{},
	}
	if err := schComposeOrdinaryClearable(page); err != nil {
		t.Fatalf("pin-level attribute treated as orphan: %v", err)
	}
	page["attributes"] = append(page["attributes"].([]any), map[string]any{"primitiveId": "a3", "ParentPrimitiveId": "gone-e9"})
	if err := schComposeOrdinaryClearable(page); err == nil {
		t.Fatal("attribute of a missing component's pin must stay an orphan")
	}
	for in, want := range map[string]string{"x-e12": "x", "x": "x", "x-e": "x-e", "x-ea": "x-ea"} {
		if got := schAttributeOwnerComponent(in); got != want {
			t.Fatalf("%s -> %s want %s", in, got, want)
		}
	}
}
