package analysis

import "unicode/utf8"

// Position is an LSP position: zero-based line and UTF-16 code unit column.
type Position struct {
	Line      int
	Character int
}

func (f *File) indexLines() {
	f.lines = []int{0}
	for i, b := range f.Text {
		if b == '\n' {
			f.lines = append(f.lines, i+1)
		}
	}
}

// PositionOf converts a byte offset into an LSP position.
func (f *File) PositionOf(offset int) Position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(f.Text) {
		offset = len(f.Text)
	}
	// Binary search for the line containing the offset.
	lo, hi := 0, len(f.lines)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if f.lines[mid] <= offset {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return Position{Line: lo, Character: utf16Len(f.Text[f.lines[lo]:offset])}
}

// OffsetOf converts an LSP position into a byte offset, clamping to the document.
func (f *File) OffsetOf(pos Position) int {
	if pos.Line < 0 {
		return 0
	}
	if pos.Line >= len(f.lines) {
		return len(f.Text)
	}
	start := f.lines[pos.Line]
	end := len(f.Text)
	if pos.Line+1 < len(f.lines) {
		end = f.lines[pos.Line+1]
	}
	line := f.Text[start:end]
	units := 0
	i := 0
	for i < len(line) && units < pos.Character {
		r, size := utf8.DecodeRune(line[i:])
		if r == '\n' || r == '\r' {
			break
		}
		if r >= 0x10000 {
			units += 2
		} else {
			units++
		}
		i += size
	}
	return start + i
}

func utf16Len(b []byte) int {
	n := 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
		b = b[size:]
	}
	return n
}
