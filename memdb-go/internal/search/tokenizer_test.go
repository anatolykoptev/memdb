package search

import (
	"testing"
)

func sliceEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTokenizeMixed_English(t *testing.T) {
	got := TokenizeMixed("Hello World!")
	want := []string{"hello", "world"}
	if !sliceEqual(got, want) {
		t.Errorf("TokenizeMixed(\"Hello World!\") = %v, want %v", got, want)
	}
}

func TestTokenizeMixed_English_Stopwords(t *testing.T) {
	got := TokenizeMixed("the is a")
	// "the" and "is" are stopwords, "a" is 1 char so filtered
	if len(got) != 0 {
		t.Errorf("TokenizeMixed(\"the is a\") = %v, want empty", got)
	}
}

func TestTokenizeMixed_Russian(t *testing.T) {
	got := TokenizeMixed("Привет мир, как дела?")
	want := []string{"привет", "мир", "дела"}
	if !sliceEqual(got, want) {
		t.Errorf("TokenizeMixed(Russian) = %v, want %v", got, want)
	}
}

func TestTokenizeMixed_Mixed(t *testing.T) {
	got := TokenizeMixed("MemDB сервер v2 deployment")
	// "v2" has 2 chars, not numeric, not a stopword -> passes all filters
	want := []string{"memdb", "сервер", "v2", "deployment"}
	if !sliceEqual(got, want) {
		t.Errorf("TokenizeMixed(Mixed) = %v, want %v", got, want)
	}
}

func TestTokenizeMixed_Empty(t *testing.T) {
	got := TokenizeMixed("")
	if got != nil {
		t.Errorf("TokenizeMixed(\"\") = %v, want nil", got)
	}
}

func TestTokenizeMixed_OnlyStopwords(t *testing.T) {
	got := TokenizeMixed("the a an")
	// "the" is stopword, "a" is 1 char, "an" is stopword
	if len(got) != 0 {
		t.Errorf("TokenizeMixed(\"the a an\") = %v, want empty", got)
	}
}

func TestBuildTSQuery(t *testing.T) {
	got := BuildTSQuery([]string{"hello", "world"})
	want := "hello | world"
	if got != want {
		t.Errorf("BuildTSQuery([hello, world]) = %q, want %q", got, want)
	}
}

func TestBuildTSQuery_Empty(t *testing.T) {
	got := BuildTSQuery([]string{})
	if got != "" {
		t.Errorf("BuildTSQuery([]) = %q, want \"\"", got)
	}
}

func TestDetectLang_English(t *testing.T) {
	got := detectLang("Hello World")
	if got != "en" {
		t.Errorf("detectLang(\"Hello World\") = %q, want \"en\"", got)
	}
}

func TestDetectLang_Russian(t *testing.T) {
	got := detectLang("Привет мир как дела")
	if got != "ru" {
		t.Errorf("detectLang(Russian text) = %q, want \"ru\"", got)
	}
}

func TestDetectLang_Chinese(t *testing.T) {
	got := detectLang("你好世界这是测试")
	if got != "zh" {
		t.Errorf("detectLang(Chinese text) = %q, want \"zh\"", got)
	}
}
