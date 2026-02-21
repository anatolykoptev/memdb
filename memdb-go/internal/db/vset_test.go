package db

import (
	"testing"
)

func TestFloat32SliceToBytes_RoundTrip(t *testing.T) {
	input := []float32{1.0, -1.0, 0.5, 0.0}
	b := float32SliceToBytes(input)
	if len(b) != len(input)*4 {
		t.Fatalf("expected %d bytes, got %d", len(input)*4, len(b))
	}
}

func TestFloat32SliceToBytes_LittleEndian(t *testing.T) {
	// 1.0 in IEEE 754 little-endian = 0x00 0x00 0x80 0x3F
	b := float32SliceToBytes([]float32{1.0})
	if b[0] != 0x00 || b[1] != 0x00 || b[2] != 0x80 || b[3] != 0x3F {
		t.Errorf("expected little-endian 1.0 = [0x00 0x00 0x80 0x3F], got %v", b)
	}
}

func TestFloat32SliceToBytes_Empty(t *testing.T) {
	b := float32SliceToBytes(nil)
	if len(b) != 0 {
		t.Errorf("expected empty bytes for nil input, got %d", len(b))
	}
	b = float32SliceToBytes([]float32{})
	if len(b) != 0 {
		t.Errorf("expected empty bytes for empty input, got %d", len(b))
	}
}

func TestExtractMemFromAttr(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "basic",
			input:    `{"m":"hello world","ts":1234567890}`,
			expected: "hello world",
		},
		{
			name:     "empty memory",
			input:    `{"m":"","ts":1234567890}`,
			expected: "",
		},
		{
			name:     "with special chars",
			input:    `{"m":"user is 28 years old","ts":9999}`,
			expected: "user is 28 years old",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "malformed - no m field",
			input:    `{"ts":1234}`,
			expected: "",
		},
		{
			name:     "too short",
			input:    `{"m":`,
			expected: "",
		},
		{
			name:     "unicode content",
			input:    `{"m":"Пользователь живёт в Москве","ts":1000}`,
			expected: "Пользователь живёт в Москве",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMemFromAttr(tt.input)
			if got != tt.expected {
				t.Errorf("extractMemFromAttr(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestVSetKey(t *testing.T) {
	if got := vsetKey("user-123"); got != "wm:v:user-123" {
		t.Errorf("vsetKey = %q, want %q", got, "wm:v:user-123")
	}
	if got := vsetKey(""); got != "wm:v:" {
		t.Errorf("vsetKey empty = %q, want %q", got, "wm:v:")
	}
}

// ---- ParseVectorString / FormatVector round-trip ----------------------------

func TestParseVectorString_Basic(t *testing.T) {
	input := "[0.1,0.2,0.3]"
	got := ParseVectorString(input)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0] < 0.099 || got[0] > 0.101 {
		t.Errorf("got[0] = %v, want ~0.1", got[0])
	}
}

func TestParseVectorString_Empty(t *testing.T) {
	if got := ParseVectorString(""); got != nil {
		t.Errorf("empty string should return nil, got %v", got)
	}
	if got := ParseVectorString("[]"); got != nil {
		t.Errorf("empty vector should return nil, got %v", got)
	}
}

func TestParseVectorString_Malformed(t *testing.T) {
	cases := []string{"not_a_vector", "[1,2,notfloat]", "[1,2,3"}
	for _, c := range cases {
		got := ParseVectorString(c)
		if got != nil {
			t.Errorf("ParseVectorString(%q) should return nil for malformed input, got %v", c, got)
		}
	}
}

func TestFormatParseRoundTrip(t *testing.T) {
	orig := []float32{0.123456, -0.789, 0.0, 1.0, -1.0}
	s := FormatVector(orig)
	got := ParseVectorString(s)
	if len(got) != len(orig) {
		t.Fatalf("round-trip len: got %d, want %d", len(got), len(orig))
	}
	for i := range orig {
		diff := got[i] - orig[i]
		if diff < 0 {
			diff = -diff
		}
		if diff > 1e-5 {
			t.Errorf("index %d: got %v, want %v (diff %v)", i, got[i], orig[i], diff)
		}
	}
}

func TestFormatVector_Brackets(t *testing.T) {
	s := FormatVector([]float32{1.0, 2.0})
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		t.Errorf("FormatVector should be wrapped in brackets, got %q", s)
	}
}

func TestFormatVector_Empty(t *testing.T) {
	s := FormatVector(nil)
	if s != "[]" {
		t.Errorf("FormatVector(nil) = %q, want %q", s, "[]")
	}
	s = FormatVector([]float32{})
	if s != "[]" {
		t.Errorf("FormatVector([]) = %q, want %q", s, "[]")
	}
}
