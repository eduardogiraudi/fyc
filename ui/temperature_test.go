package ui

import (
	"strings"
	"testing"
)

func TestRenderProviderInfoTemperatureLabel(t *testing.T) {
	for _, temperature := range []string{"0.00", "0.80", "Omitted"} {
		line := stripANSI(RenderProviderInfo("test (model)", temperature, "manual"))
		for _, want := range []string{"[Provider: test (model)]", "[Temp: " + temperature + "]", "[Mode: manual]"} {
			if !strings.Contains(line, want) {
				t.Fatalf("missing %q in %q", want, line)
			}
		}
	}
}
