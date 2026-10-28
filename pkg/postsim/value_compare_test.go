package postsim

import "testing"

func TestSameValue(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1M", "1m", false}, // megohm vs milliohm
		{"1M", "1M", true},
		{"1M", "1000k", true},
		{"4k7", "4.7k", true},
		{"4K7", "4700", true},
		{"4k7", "4.7kΩ", true},
		{"100nF", "100n", true},
		{"100nF", "0.1uF", true},
		{"100nF", "100nH", false},
		{"2u2", "2.2µF", true},
		{"4R7", "4.7Ω", true},
		{"10R", "10ohm", true},
		{"10k", "10K", true},
		{"10k", "1k", false},
		{"25MHz", "25mHz", false},
		{"10k NTC", "10k NTC", true}, // not a plain value: exact text
		{"10k NTC", "10k ntc", false},
		{"CR2032", "CR2032", true},
		{"CR2032", "cr2032", false},
	} {
		if got := sameValue(c.a, c.b); got != c.want {
			t.Errorf("sameValue(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
