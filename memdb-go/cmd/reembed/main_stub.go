//go:build !cgo

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "reembed: built without CGO, ONNX embedder unavailable")
	fmt.Fprintln(os.Stderr, "reembed: will be updated to use HTTP embedder in a future release")
	os.Exit(1)
}
