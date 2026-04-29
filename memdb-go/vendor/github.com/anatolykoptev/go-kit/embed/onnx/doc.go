// Package onnx provides a local ONNX Runtime embedder backend.
//
// This package is split out of the parent go-kit/embed package to keep cgo
// linkage opt-in. It depends on:
//
//   - github.com/yalue/onnxruntime_go — Go bindings for ONNX Runtime
//   - github.com/daulet/tokenizers     — Go bindings for HuggingFace tokenizers
//   - libonnxruntime.so (host)         — at /usr/lib/libonnxruntime.so
//   - libtokenizers.a (host)           — bundled with daulet/tokenizers
//
// Pure-Go callers (HTTP / Ollama / Voyage) using github.com/anatolykoptev/go-kit/embed
// do NOT pull these in. Only services that need on-host ONNX inference
// (currently: memdb-go's MEMDB_EMBEDDER_TYPE=onnx mode) import this package.
//
// CGO build tag: when CGO_ENABLED=0 the implementation falls back to a stub
// that returns an error from every method (mirrors the memdb-go behaviour).
//
// Usage:
//
//	import (
//	    "github.com/anatolykoptev/go-kit/embed"
//	    "github.com/anatolykoptev/go-kit/embed/onnx"
//	)
//
//	cfg := onnx.Config{
//	    ModelDir: "/models/multilingual-e5-large",
//	    Model:    onnx.DefaultModelConfig(),
//	}
//	e, err := onnx.New(cfg, logger)
//	// e implements embed.Embedder and can be registered with embed.Registry.
package onnx
