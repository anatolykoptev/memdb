package lang

import "testing"

func TestDetect_Empty(t *testing.T) {
	if got := Detect(""); got != "en" {
		t.Errorf("Detect(\"\") = %q, want \"en\"", got)
	}
}

func TestDetect_PureASCIIEnglish(t *testing.T) {
	if got := Detect("Hello, how are you today?"); got != "en" {
		t.Errorf("Detect(ascii english) = %q, want \"en\"", got)
	}
}

func TestDetect_PureCyrillic(t *testing.T) {
	// "Привет мир" — all Cyrillic letters → well above 30% threshold.
	if got := Detect("Привет мир"); got != "ru" {
		t.Errorf("Detect(\"Привет мир\") = %q, want \"ru\"", got)
	}
}

func TestDetect_MajorityCyrillic(t *testing.T) {
	// Synthetic string: 6 Cyrillic + 4 ASCII letters = 60% Cyrillic > 30%.
	if got := Detect("Привет test"); got != "ru" {
		t.Errorf("Detect(50%%+ Cyrillic) = %q, want \"ru\"", got)
	}
}

func TestDetect_MixedBelowCyrillicThreshold(t *testing.T) {
	// "Hello world from Moscow город" — two Cyrillic words out of 5 = ~25% Cyrillic
	// (5 chars Cyrillic in "город" vs ~25 total letter+digit chars).
	// The original chat_lang test asserts this returns "en".
	if got := Detect("Hello world from Moscow город"); got != "en" {
		t.Errorf("Detect(mixed below threshold) = %q, want \"en\"", got)
	}
}

func TestDetect_PureCJK(t *testing.T) {
	if got := Detect("你好世界"); got != "zh" {
		t.Errorf("Detect(\"你好世界\") = %q, want \"zh\"", got)
	}
}

func TestDetect_MajorityCJK(t *testing.T) {
	// CJK chars dominate — 4 han vs 3 ASCII letters.
	if got := Detect("你好世界 abc"); got != "zh" {
		t.Errorf("Detect(majority CJK) = %q, want \"zh\"", got)
	}
}

func TestDetect_OnlyDigits(t *testing.T) {
	// Digits count toward total but not toward cjk/cyrillic → result is "en".
	if got := Detect("12345"); got != "en" {
		t.Errorf("Detect(\"12345\") = %q, want \"en\"", got)
	}
}

func TestDetect_OnlyPunctuation(t *testing.T) {
	// total == 0 → "en"
	if got := Detect("... !!!"); got != "en" {
		t.Errorf("Detect(punctuation only) = %q, want \"en\"", got)
	}
}
