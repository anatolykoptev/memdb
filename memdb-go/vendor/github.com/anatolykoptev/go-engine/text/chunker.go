package text

import (
	"strings"
	"unicode/utf8"
)

// Chunker splits text into overlapping chunks.
type Chunker interface {
	Chunk(text string) []string
}

// CharacterChunker splits text at word boundaries near chunkSize runes,
// with overlap runes shared between adjacent chunks. UTF-8 safe.
type CharacterChunker struct {
	chunkSize int
	overlap   int
}

// NewCharacterChunker creates a CharacterChunker. If overlap >= chunkSize,
// it is clamped to chunkSize/4 to ensure forward progress.
func NewCharacterChunker(chunkSize, overlap int) *CharacterChunker {
	if overlap >= chunkSize {
		overlap = chunkSize / 4
	}
	return &CharacterChunker{chunkSize: chunkSize, overlap: overlap}
}

// Chunk splits text into chunks of at most chunkSize runes, splitting at word
// boundaries where possible, with overlap runes shared between adjacent chunks.
// Returns nil for empty input. Single-chunk result for text <= chunkSize runes.
func (c *CharacterChunker) Chunk(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	runes := []rune(text)
	if len(runes) <= c.chunkSize {
		return []string{text}
	}

	var chunks []string
	pos := 0
	for pos < len(runes) {
		chunk, next := c.nextChunk(runes, pos)
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		pos = next
	}
	return chunks
}

// NeedsChunking reports whether text is long enough to produce multiple chunks.
// Use before Chunk() to skip processing for short content.
func (c *CharacterChunker) NeedsChunking(text string) bool {
	return utf8.RuneCountInString(text) > c.chunkSize
}

// nextChunk returns the next chunk string and the position to continue from.
func (c *CharacterChunker) nextChunk(runes []rune, pos int) (string, int) {
	end := pos + c.chunkSize
	if end >= len(runes) {
		return strings.TrimSpace(string(runes[pos:])), len(runes)
	}

	splitAt := c.findWordBoundary(runes, pos, end)
	if splitAt >= 0 {
		chunk := strings.TrimSpace(string(runes[pos:splitAt]))
		next := splitAt + 1 - c.overlap // skip the space, step back by overlap
		if next <= pos {
			next = pos + 1
		}
		return chunk, next
	}

	// Force-split: no word boundary in window.
	chunk := strings.TrimSpace(string(runes[pos:end]))
	next := end - c.overlap
	if next <= pos {
		next = pos + 1
	}
	return chunk, next
}

// findWordBoundary searches for a space in [halfPoint, end) scanning backward.
// Returns the space index, or -1 if none found.
func (c *CharacterChunker) findWordBoundary(runes []rune, pos, end int) int {
	halfPoint := pos + c.chunkSize/2
	for i := end - 1; i >= halfPoint; i-- {
		if runes[i] == ' ' {
			return i
		}
	}
	return -1
}
