package llm

import "testing"

func TestDetectLang(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Lang
	}{
		{"english", "Hello, I work at Company A as a developer", LangEN},
		{"chinese", "你好，我在A公司工作，是一名开发人员", LangZH},
		{"chinese with role prefix", "user: 你好世界\nassistant: 你好", LangZH},
		{"mixed mostly english", "user: I went to the store today 你好", LangEN},
		{"russian", "Привет, я работаю в компании разработчиком", LangRU},
		{"empty", "", LangEN},
		{"pure chinese no ascii", "你好世界这是一个测试", LangZH},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectLang(tt.text)
			if got != tt.want {
				t.Errorf("DetectLang(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
