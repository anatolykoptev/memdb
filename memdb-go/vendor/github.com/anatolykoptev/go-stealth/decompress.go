package stealth

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"strings"
)

// decompressBody transparently decompresses gzip or deflate response bodies.
// If the content-encoding header indicates compression, the body is decompressed.
// Falls back to gzip magic-byte detection when headers are missing.
func decompressBody(data []byte, contentEncoding string) ([]byte, error) {
	enc := strings.ToLower(strings.TrimSpace(contentEncoding))

	switch {
	case strings.Contains(enc, "gzip"):
		return decompressGzip(data)
	case strings.Contains(enc, "deflate"):
		return decompressDeflate(data)
	case len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b:
		// Gzip magic bytes — header might be stripped by proxy.
		return decompressGzip(data)
	default:
		return data, nil
	}
}

func decompressGzip(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return data, nil // not valid gzip — return raw
	}
	defer r.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		return data, nil // decompression failed — return raw
	}
	return out, nil
}

func decompressDeflate(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return data, nil
	}
	defer r.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		return data, nil
	}
	return out, nil
}
