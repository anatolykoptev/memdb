# LLM Providers

MemDB uses any OpenAI-compatible chat completions API. Configure via env vars.

## OpenAI (default)

```
export MEMDB_LLM_PROXY_URL=https://api.openai.com/v1
export MEMDB_LLM_API_KEY=sk-...
export MEMDB_LLM_MODEL=gpt-4o-mini
```

## Anthropic (via LiteLLM or OpenRouter)

Anthropic's native API is not OpenAI-compatible. Two options:

- **LiteLLM** proxy (self-host): https://github.com/BerriAI/litellm
- **OpenRouter**: https://openrouter.ai — OpenAI-compatible, supports Claude

```
export MEMDB_LLM_PROXY_URL=https://openrouter.ai/api/v1
export MEMDB_LLM_API_KEY=sk-or-...
export MEMDB_LLM_MODEL=anthropic/claude-3-5-sonnet
```

## Google Gemini

Gemini native API is not OpenAI-compatible. Use OpenRouter or Google's
OpenAI-compatibility layer at `https://generativelanguage.googleapis.com/v1beta/openai`
— see [Gemini docs](https://ai.google.dev/gemini-api/docs/openai).

## Ollama (local)

```
export MEMDB_LLM_PROXY_URL=http://localhost:11434/v1
export MEMDB_LLM_API_KEY=ollama
export MEMDB_LLM_MODEL=llama3.2
```

## Custom proxies (LiteLLM, CLIProxyAPI, etc.)

Any service exposing `/v1/chat/completions` works. Set `MEMDB_LLM_PROXY_URL` to its base URL.
