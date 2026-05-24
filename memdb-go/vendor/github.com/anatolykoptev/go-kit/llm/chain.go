package llm

import "strings"

// ParseModelFallbackChain парсит CSV-список моделей из env (например
// LLM_MODEL_FALLBACK). Тримит whitespace, отбрасывает пустые токены,
// дедупит, sanitize-ит каждое имя по Prometheus-label-safe charset
// [A-Za-z0-9._/-]. Небезопасные токены отбрасываются молча — caller
// должен валидировать конфиг отдельно если надо логировать предупреждения.
//
// Порядок сохраняется (FIFO по CSV).
func ParseModelFallbackChain(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	var out []string
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !isSafeModelName(p) {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// isSafeModelName проверяет что имя модели состоит только из
// Prometheus-label-safe символов: A-Z a-z 0-9 . _ / -
func isSafeModelName(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.', r == '_', r == '/', r == '-':
		default:
			return false
		}
	}
	return true
}

// BuildModelChainEndpoints генерирует []Endpoint для WithEndpoints, где
// все endpoints используют один baseURL+apiKey но разные Model. Primary
// model идёт первой; модели из chain совпадающие с primary отбрасываются
// чтобы не делать дублирующих попыток. Пустая primary пропускается.
//
// Use case: cliproxyapi на :8317 с одним CLI_PROXY_API_KEY роутит запросы
// к разным upstream-провайдерам (gemini/cerebras/groq/openrouter) по
// model id. CSV-chain тут = cross-provider failure-domain.
//
// ВАЖНО: каждый endpoint выполняется через doWithRetry (max retries по
// конфигу клиента, default 3). Длинная chain × default retries =
// большое wall time на полностью провалившемся запросе. Для chain'ов
// длиной >2 рекомендуется WithMaxRetries(1) — модели сами по себе
// уже работают как retry layer.
func BuildModelChainEndpoints(baseURL, apiKey, primary string, chain []string) []Endpoint {
	out := make([]Endpoint, 0, 1+len(chain))
	seen := make(map[string]struct{}, 1+len(chain))
	if primary != "" {
		out = append(out, Endpoint{URL: baseURL, Key: apiKey, Model: primary})
		seen[primary] = struct{}{}
	}
	for _, m := range chain {
		if m == "" {
			continue
		}
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, Endpoint{URL: baseURL, Key: apiKey, Model: m})
	}
	return out
}
