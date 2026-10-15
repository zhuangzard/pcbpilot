package app

import "testing"

func TestExportImageFormatFollowsOutExtension(t *testing.T) {
	for _, c := range []struct{ format, out, want string }{
		{"", "page.png", "png"}, {"", "p.PDF", "pdf"}, {"", "page", ""}, {"", "x.txt", ""},
		{"svg", "page.svg", "svg"}, {"png", "", "png"},
	} {
		if got, err := exportImageFormat(c.format, c.out); err != nil || got != c.want {
			t.Fatalf("%q %q → %q %v, want %q", c.format, c.out, got, err, c.want)
		}
	}
	if _, err := exportImageFormat("svg", "page.png"); err == nil {
		t.Fatal("svg into .png must be refused")
	}
}
