package app

import "testing"

// 两条不同网、靠得很近的平行走线 —— 3W 判据的经典触发形态。
func couplingPair() []pcbTrack {
	return []pcbTrack{
		{ID: "t1", Net: "USB_DP", Layer: 1, Width: 10, X1: 0, Y1: 0, X2: 200, Y2: 0},
		{ID: "t2", Net: "USB_DM", Layer: 1, Width: 10, X1: 0, Y1: 12, X2: 200, Y2: 12},
	}
}

// 没有声明差分对时,行为必须与从前完全一致 —— 这条是防回归的。
func TestParallelCouplingWithoutDeclaredPairStillWarns(t *testing.T) {
	if got := len(findParallelCoupling(couplingPair(), 3, nil)); got != 1 {
		t.Fatalf("coupling findings = %d, want 1 (no pair declared → the rule must behave as before)", got)
	}
}

// 声明成差分对之后就不该再报:紧耦合正是这对线的设计目的,
// 而 3W 规则说的是「一对(或一根)对上其他网」,从来不是「一对之内」。
// 不跳过的话,这条警告没有任何合法布线能消掉。
func TestParallelCouplingSkipsDeclaredDiffPair(t *testing.T) {
	pairs := []pcbDiffPair{{Positive: "USB_DP", Negative: "USB_DM"}}
	if got := len(findParallelCoupling(couplingPair(), 3, pairs)); got != 0 {
		t.Fatalf("coupling findings = %d, want 0 for a declared pair", got)
	}
	// 顺序不能影响判断:finding 里的网名是排序过的,规则不能依赖谁是正端。
	swapped := []pcbDiffPair{{Positive: "USB_DM", Negative: "USB_DP"}}
	if got := len(findParallelCoupling(couplingPair(), 3, swapped)); got != 0 {
		t.Fatalf("coupling findings = %d, want 0 regardless of which net is positive", got)
	}
}

// 对内跳过,不代表对外免检 —— 差分对旁边贴着第三个网,仍然是真的串扰风险。
func TestParallelCouplingStillChecksPairAgainstOtherNets(t *testing.T) {
	tracks := append(couplingPair(),
		pcbTrack{ID: "t3", Net: "SPI_CLK", Layer: 1, Width: 10, X1: 0, Y1: 24, X2: 200, Y2: 24})
	pairs := []pcbDiffPair{{Positive: "USB_DP", Negative: "USB_DM"}}
	got := findParallelCoupling(tracks, 3, pairs)
	// 不断言条数 —— 那取决于第三根线离两个成员各多远(这里两边都在 3W 内)。
	// 要断言的是语义:对内那一条消失,对外那些还在。
	if len(got) == 0 {
		t.Fatal("a third net running inside 3W of the pair is real crosstalk and must still be reported")
	}
	for _, f := range got {
		if len(f.Nets) == 2 &&
			((f.Nets[0] == "USB_DM" && f.Nets[1] == "USB_DP") || (f.Nets[0] == "USB_DP" && f.Nets[1] == "USB_DM")) {
			t.Fatalf("intra-pair coupling must not be reported, got %v", f.Nets)
		}
		if f.Nets[0] != "SPI_CLK" && f.Nets[1] != "SPI_CLK" {
			t.Fatalf("every remaining finding must involve the third net, got %v", f.Nets)
		}
	}
}

// 只有「两个成员正好是同一对」才跳过;各属不同对的两根线照报。
func TestParallelCouplingDoesNotSkipAcrossDifferentPairs(t *testing.T) {
	pairs := []pcbDiffPair{
		{Positive: "USB_DP", Negative: "OTHER_N"},
		{Positive: "OTHER_P", Negative: "USB_DM"},
	}
	if got := len(findParallelCoupling(couplingPair(), 3, pairs)); got != 1 {
		t.Fatalf("coupling findings = %d, want 1 — these two nets belong to different pairs", got)
	}
}
