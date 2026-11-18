package mentions

import (
	"reflect"
	"testing"
)

func TestScan(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"inspect @test.py and @foto.png", []string{"test.py", "foto.png"}},
		{"α\n@\"my dir/test.py\"", []string{"my dir/test.py"}},
		{"a@b.com https://a/@test.py \\@literal.py `@code.py`", nil},
		{"```python\n@test.py\n```\n@real.py", []string{"real.py"}},
		{"~~~\n@test.py\n~~~\n@real.py", []string{"real.py"}},
		{"`` @literal ` @literal `` @real.py", []string{"real.py"}},
		{"@\"unfinished", nil},
	} {
		var got []string
		for _, ref := range Scan(tc.text, false) {
			got = append(got, ref.Path)
			if string([]rune(tc.text)[ref.Start:ref.End]) != Tag(ref.Path) {
				t.Errorf("bad offsets: %+v", ref)
			}
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Scan(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestActive(t *testing.T) {
	for _, text := range []string{"inspect @", "inspect @te", "α\n@\"my dir/te"} {
		if _, ok := Active(text); !ok {
			t.Errorf("not active: %q", text)
		}
	}
	for _, text := range []string{"@test.py ", "email@example.com", "`@code", "\\@literal"} {
		if _, ok := Active(text); ok {
			t.Errorf("unexpected active: %q", text)
		}
	}
}
