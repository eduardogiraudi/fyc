package providers

import "testing"

func TestDiffCraftLabel(t *testing.T) {
	cases := []struct {
		name    string
		rawArgs string
		want    string
	}{
		{"path known", `{"path":"main.go","replacement":"x"}`, "patching file main.go"},
		{"path not yet in the stream", `{"replacement":"x"}`, "patching file"},
		{"empty args", ``, "patching file"},
		{"blank path", `{"path":"   "}`, "patching file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diffCraftLabel(tc.rawArgs); got != tc.want {
				t.Fatalf("diffCraftLabel(%q) = %q, want %q", tc.rawArgs, got, tc.want)
			}
		})
	}
}
