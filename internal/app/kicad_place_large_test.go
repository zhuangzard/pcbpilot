package app

import (
	"testing"
	"time"
)

func TestPlaceLargeDefaults(t *testing.T) {
	const small = placeLargeBoard
	if b, s, ch := placeLargeDefaults(small, 8*time.Minute, 6, false, false, 4); ch || b != 8*time.Minute || s != 6 {
		t.Errorf("a small board must not change: %v %d %v", b, s, ch)
	}
	if b, s, ch := placeLargeDefaults(small+1, 8*time.Minute, 6, false, false, 4); !ch || b != 30*time.Minute || s != 3 {
		t.Errorf("large: %v %d %v", b, s, ch)
	}
	if b, s, ch := placeLargeDefaults(small+1, 8*time.Minute, 6, true, true, 4); ch || b != 8*time.Minute || s != 6 {
		t.Errorf("explicit flags win: %v %d %v", b, s, ch)
	}
	if _, s, _ := placeLargeDefaults(small+1, 8*time.Minute, 6, false, false, 1); s != 1 {
		t.Errorf("one core: %d", s)
	}
}
