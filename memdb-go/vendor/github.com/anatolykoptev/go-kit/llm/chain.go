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

// ProxySpec defines one proxy endpoint (URL + API key) for multi-proxy rotation.
// Used by BuildMultiProxyEndpoints to build a cross-product of proxies × models.
type ProxySpec struct {
	URL string
	Key string
}

// BuildMultiProxyEndpoints generates []Endpoint for WithEndpoints across
// multiple proxy URLs. For each proxy, it builds the full model chain
// (primary first, then fallbacks, deduped). Proxies are tried in order:
// proxy1:primary, proxy1:model2, ..., proxy2:primary, proxy2:model2, ...
//
// Use case: local cliproxyapi (localhost:8317) + remote cliproxyapi
// (10.9.0.2:8317 over WireGuard). Local is tried first for every model;
// if the local proxy is down (connection refused / timeout), the chain
// advances to the remote proxy for the same model, then continues.
//
// This gives proxy-level redundancy on top of the existing model-level
// fallback: a single proxy outage no longer takes down the entire chain.
//
// If proxies has 1 entry, the result is identical to BuildModelChainEndpoints
// (same URL, same key, same model order).
func BuildMultiProxyEndpoints(proxies []ProxySpec, primary string, chain []string) []Endpoint {
	if len(proxies) == 0 {
		return nil
	}
	if len(proxies) == 1 {
		return BuildModelChainEndpoints(proxies[0].URL, proxies[0].Key, primary, chain)
	}
	out := make([]Endpoint, 0, len(proxies)*(1+len(chain)))
	for _, p := range proxies {
		seen := make(map[string]struct{}, 1+len(chain))
		if primary != "" {
			out = append(out, Endpoint{URL: p.URL, Key: p.Key, Model: primary})
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
			out = append(out, Endpoint{URL: p.URL, Key: p.Key, Model: m})
		}
	}
	return out
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
