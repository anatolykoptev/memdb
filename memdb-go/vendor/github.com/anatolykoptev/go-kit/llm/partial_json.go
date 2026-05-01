package llm

// partialJSON attempts to close unclosed JSON brackets/strings to make
// an incomplete JSON string parseable. This enables progressive parsing
// of streaming JSON output.
func partialJSON(s string) string {
	if s == "" {
		return `""`
	}

	var (
		inString bool
		escaped  bool
		stack    []byte // tracks open brackets: '{' and '['
	)

	for i := range len(s) {
		ch := s[i]

		if escaped {
			escaped = false
			continue
		}

		if inString {
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}':
			if len(stack) > 0 && stack[len(stack)-1] == '}' {
				stack = stack[:len(stack)-1]
			}
		case ']':
			if len(stack) > 0 && stack[len(stack)-1] == ']' {
				stack = stack[:len(stack)-1]
			}
		}
	}

	if !inString && len(stack) == 0 {
		return s
	}

	// Build the closing sequence.
	buf := make([]byte, 0, len(s)+1+len(stack))
	buf = append(buf, s...)
	if inString {
		buf = append(buf, '"')
	}
	for i := len(stack) - 1; i >= 0; i-- {
		buf = append(buf, stack[i])
	}

	return string(buf)
}
