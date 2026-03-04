package text

import "strings"

// QueryType determines how the LLM should structure its answer.
type QueryType int

const (
	QtGeneral    QueryType = iota // default
	QtFact                        // single fact: price, date, number
	QtComparison                  // X vs Y, differences
	QtList                        // enumerate items
	QtHowTo                       // step-by-step guide
)

// DetectQueryType classifies query by simple pattern matching.
// Pure string matching, no IO — avoids asking the LLM to figure out format.
func DetectQueryType(query string) QueryType {
	q := strings.ToLower(query)

	factPatterns := []string{
		"курс", "цена", "стоимость", "сколько стоит", "price",
		"когда", "какой год", "дата", "who is", "кто такой", "кто это",
		"what is the", "какой размер", "население",
	}
	for _, p := range factPatterns {
		if strings.Contains(q, p) {
			return QtFact
		}
	}

	compPatterns := []string{
		" vs ", " versus ", " или ", "сравнение", "сравни",
		"отличи", "разниц", "лучше", "compare", "difference",
		"чем отличается", "что лучше", "что выбрать",
	}
	for _, p := range compPatterns {
		if strings.Contains(q, p) {
			return QtComparison
		}
	}

	listPatterns := []string{
		"какие", "перечисли", "список", "назови", "топ-", "top ",
		"лучшие", "best", "list", "enumerate", "фреймворки", "frameworks",
		"инструменты", "tools", "альтернативы", "alternatives",
	}
	for _, p := range listPatterns {
		if strings.Contains(q, p) {
			return QtList
		}
	}

	howtoPatterns := []string{
		"как ", "how to", "how do", "настроить", "установить",
		"сделать", "создать", "setup", "install", "configure",
		"пошагово", "step by step", "guide", "tutorial",
	}
	for _, p := range howtoPatterns {
		if strings.Contains(q, p) {
			return QtHowTo
		}
	}

	return QtGeneral
}

// QueryDomain determines WHAT domain the query belongs to,
// so smart_search can route to specialized sources.
type QueryDomain int

const (
	QdGeneral     QueryDomain = iota // default web search
	QdWordPress                      // WordPress dev docs
	QdClaudeCode                     // Claude Code / Anthropic plugins
	QdGitHubRepo                     // GitHub repository discovery
	QdLibDocs                        // library/framework documentation (Context7)
	QdHuggingFace                    // HuggingFace model/dataset discovery
)

// DetectQueryDomain classifies query by domain-specific patterns.
func DetectQueryDomain(query string) QueryDomain {
	q := strings.ToLower(query)

	wpPatterns := []string{
		"wordpress", "wp_", "wp-cli", "add_action", "add_filter",
		"do_action", "apply_filters", "gutenberg", "woocommerce",
		"wp_enqueue", "wp_query", "the_content", "the_title",
		"get_post", "shortcode", "wp rest api", "wp-json",
		"wp_hook", "wp_register", "register_post_type",
		"register_taxonomy", "have_posts", "the_loop",
	}
	for _, p := range wpPatterns {
		if strings.Contains(q, p) {
			return QdWordPress
		}
	}

	ccPatterns := []string{
		"claude code", "claude-code", "anthropic plugin",
		"pretooluse", "posttooluse", "claude.md",
		"claude hook", "claude skill", "claude command",
		"knowledge-work-plugins", "claude code plugin",
	}
	for _, p := range ccPatterns {
		if strings.Contains(q, p) {
			return QdClaudeCode
		}
	}

	ghPatterns := []string{
		"library for", "package for", "github repo",
		"find repo", "recommend library", "best library",
		"suggest package", "npm package for", "go module for",
		"pip package for", "crate for", "найди репо",
		"альтернативы", "alternatives to",
	}
	for _, p := range ghPatterns {
		if strings.Contains(q, p) {
			return QdGitHubRepo
		}
	}

	hfPatterns := []string{
		"huggingface", "hugging face", "hf model", "hf dataset",
		"найди модель", "найти модель", "модель для", "лучшую модель",
		"model for ", "best model for", "find model", "речевая модель",
		"speech model", "vision model", "language model for",
		"gguf model", "quantized model", "локальная llm", "local llm",
		"hf hub", "hub model", "open source model",
	}
	for _, p := range hfPatterns {
		if strings.Contains(q, p) {
			return QdHuggingFace
		}
	}

	if ExtractLibraryName(query) != "" {
		return QdLibDocs
	}

	return QdGeneral
}

// ExtractLibraryName detects a known library/framework name from a query string.
// Returns the canonical library ID or "" if none matched.
func ExtractLibraryName(query string) string {
	q := strings.ToLower(query)
	libraries := []struct{ pattern, name string }{
		{"next.js", "next.js"}, {"nextjs", "next.js"},
		{"react native", "react-native"},
		{"react", "react"}, {"vue.js", "vue"}, {"vuejs", "vue"}, {"vue", "vue"},
		{"angular", "angular"}, {"svelte", "svelte"}, {"solid.js", "solid"},
		{"express.js", "express"}, {"express", "express"},
		{"nestjs", "nestjs"}, {"nuxt", "nuxt"}, {"remix", "remix"},
		{"tailwindcss", "tailwindcss"}, {"tailwind", "tailwindcss"},
		{"prisma", "prisma"}, {"drizzle", "drizzle-orm"},
		{"zod", "zod"}, {"trpc", "trpc"},
		{"playwright", "playwright"}, {"puppeteer", "puppeteer"},
		{"jest", "jest"}, {"vitest", "vitest"}, {"cypress", "cypress"},
		{"webpack", "webpack"}, {"vite", "vite"}, {"esbuild", "esbuild"},
		{"three.js", "three.js"}, {"threejs", "three.js"}, {"d3.js", "d3"}, {"d3", "d3"},
		{"socket.io", "socket.io"}, {"axios", "axios"},
		{"tanstack", "tanstack-query"}, {"react-query", "tanstack-query"},
		{"zustand", "zustand"}, {"jotai", "jotai"}, {"redux", "redux"},
		{"shadcn", "shadcn-ui"}, {"radix", "radix-ui"},
		{"astro", "astro"}, {"gatsby", "gatsby"},
		{"deno", "deno"}, {"bun", "bun"},
		{"hono", "hono"}, {"fastify", "fastify"}, {"koa", "koa"},
		{"fastapi", "fastapi"}, {"django", "django"}, {"flask", "flask"},
		{"sqlalchemy", "sqlalchemy"}, {"pydantic", "pydantic"},
		{"celery", "celery"}, {"pytest", "pytest"},
		{"langchain", "langchain"}, {"llamaindex", "llama-index"},
		{"pandas", "pandas"}, {"numpy", "numpy"}, {"scipy", "scipy"},
		{"pytorch", "pytorch"}, {"tensorflow", "tensorflow"},
		{"scikit-learn", "scikit-learn"}, {"sklearn", "scikit-learn"},
		{"matplotlib", "matplotlib"}, {"streamlit", "streamlit"},
		{"httpx", "httpx"}, {"aiohttp", "aiohttp"},
		{"beautifulsoup", "beautifulsoup4"}, {"scrapy", "scrapy"},
		{"gin ", "gin-gonic"}, {"gin-gonic", "gin-gonic"},
		{"echo ", "echo"}, {"fiber ", "fiber"},
		{"gorm", "gorm"}, {"ent ", "ent"},
		{"cobra", "cobra"}, {"viper", "viper"},
		{"tokio", "tokio"}, {"actix", "actix-web"}, {"axum", "axum"},
		{"serde", "serde"}, {"reqwest", "reqwest"},
		{"supabase", "supabase"}, {"firebase", "firebase"},
		{"mongodb", "mongodb"}, {"mongoose", "mongoose"},
		{"redis", "redis"}, {"elasticsearch", "elasticsearch"},
		{"docker", "docker"}, {"kubernetes", "kubernetes"}, {"k8s", "kubernetes"},
		{"terraform", "terraform"}, {"pulumi", "pulumi"},
		{"htmx", "htmx"}, {"alpine.js", "alpinejs"}, {"alpinejs", "alpinejs"},
		{"graphql", "graphql"}, {"apollo", "apollo-client"},
		{"stripe", "stripe"}, {"auth0", "auth0"},
	}
	for _, lib := range libraries {
		if strings.Contains(q, lib.pattern) {
			return lib.name
		}
	}
	return ""
}
