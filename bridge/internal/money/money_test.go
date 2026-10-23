package money

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		input string
		want  Cents
		ok    bool
	}{
		{"10", 1000, true},
		{"10.5", 1050, true},
		{"10.50", 1050, true},
		{"0.01", 1, true},
		{"-1.25", -125, true},
		{"1.234", 0, false},
		{"", 0, false},
	}
	for _, tc := range tests {
		got, err := Parse(tc.input)
		if tc.ok && err != nil {
			t.Fatalf("Parse(%q): %v", tc.input, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("Parse(%q): expected error", tc.input)
		}
		if tc.ok && got != tc.want {
			t.Fatalf("Parse(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestParseJSON(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  Cents
	}{
		{`"12.30"`, 1230},
		{`12.3`, 1230},
		{`0`, 0},
	} {
		got, err := ParseJSON([]byte(tc.input))
		if err != nil {
			t.Fatalf("ParseJSON(%s): %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("ParseJSON(%s) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestString(t *testing.T) {
	if got := Cents(1050).String(); got != "10.50" {
		t.Fatalf("String() = %q", got)
	}
}
