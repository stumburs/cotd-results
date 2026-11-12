package nadeo

import "testing"

func TestParseMatchSize(t *testing.T) {
	raw := `{"version":1,"rounds":[{"matchGeneratorData":{"matchSize":64,"matchGeneratorType":"daily_cup"},"matchGeneratorType":"daily_cup"}]}`
	if got := ParseMatchSize(raw); got != 64 {
		t.Errorf("ParseMatchSize = %d, want 64", got)
	}
	if got := ParseMatchSize(""); got != 64 {
		t.Errorf("ParseMatchSize(empty) = %d, want 64", got)
	}
	if got := ParseMatchSize(`{"rounds":[{"matchGeneratorData":{"matchSize":32}}]}`); got != 32 {
		t.Errorf("ParseMatchSize = %d, want 32", got)
	}
}
