package notify

import (
	"testing"
)

func TestParseTime(t *testing.T) {
	tests := []struct {
		input string
		h, m  int
	}{
		{"22:00", 22, 0},
		{"07:30", 7, 30},
		{"00:00", 0, 0},
		{"", 0, 0},
	}
	for _, tt := range tests {
		h, m := parseTime(tt.input)
		if h != tt.h || m != tt.m {
			t.Errorf("parseTime(%q) = (%d, %d), want (%d, %d)", tt.input, h, m, tt.h, tt.m)
		}
	}
}
