package xps

import "encoding/binary"

// opentype.Parse also opens a WOFF or WOFF2 file and inflates it (256 MiB at
// most), so the size of a font part says nothing of what the font weighs once it
// is decoded. The table directory of such a file declares every table's length
// and the library inflates exactly that much (it refuses a stream that runs
// past it), so the directory is what the budget is charged from, before the
// library inflates anything.
//
// ECMA-388 §9.1.7 names TrueType and OpenType fonts (and their obfuscated form)
// as the font parts of a package; a WOFF is the library's convenience, not the
// format's, and it is read here under the same ceilings as the plain font.

const (
	sigWOFF1 = 0x774F4646 // "wOFF"
	sigWOFF2 = 0x774F4632 // "wOF2"
)

// woff2Tags are the 6-bit tag indexes of a WOFF2 directory entry that name an
// instruction table (WOFF2 §5.1).
var woff2Tags = map[byte]string{8: "cvt ", 9: "fpgm", 12: "prep"}

// isInstruction is whether a table tag is one the subsetter copies whole.
func isInstruction(tag string) bool { return tag == "fpgm" || tag == "prep" || tag == "cvt " }

// A woffTable is one directory entry: whether it is an instruction table, and
// the length it has once decoded.
type woffTable struct {
	instr bool
	orig  int64
}

// woffTables reads the table directory of a WOFF or WOFF2 file; ok is false for
// any other bytes and for a directory the bytes do not hold (the library
// refuses those itself, before it inflates).
func woffTables(b []byte) (tabs []woffTable, ok bool) {
	if len(b) < 48 || binary.BigEndian.Uint16(b[12:]) == 0 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(b[12:]))
	switch binary.BigEndian.Uint32(b) {
	case sigWOFF1:
		for i := 0; i < n; i++ {
			e := 44 + 20*i
			if e+20 > len(b) {
				return nil, false
			}
			tabs = append(tabs, woffTable{isInstruction(string(b[e : e+4])), int64(binary.BigEndian.Uint32(b[e+12:]))})
		}
		return tabs, true
	case sigWOFF2:
		i := 48
		for k := 0; k < n; k++ {
			if i >= len(b) {
				return nil, false
			}
			flags := b[i]
			i++
			idx := flags & 0x3f
			tag := woff2Tags[idx]
			if idx == 0x3f {
				// A tag cut short names nothing, and the length after it is
				// what is missing: readBase128 refuses it.
				tag = string(b[i:min(i+4, len(b))])
				i += 4
			}
			orig, next, ok := readBase128(b, i)
			if !ok {
				return nil, false
			}
			i = next
			// A transformed table carries its stream length too (glyf and loca are
			// transformed at version 0, every other table at any other version).
			if (flags>>6 != 0) != (idx == 10 || idx == 11) {
				if _, next, ok = readBase128(b, i); !ok {
					return nil, false
				}
				i = next
			}
			tabs = append(tabs, woffTable{isInstruction(tag), int64(orig)})
		}
		return tabs, true
	}
	return nil, false
}

// readBase128 reads a WOFF2 UIntBase128 at b[i:]; ok is false when it does not
// fit the bytes or 32 bits.
func readBase128(b []byte, i int) (v uint32, next int, ok bool) {
	for k := 0; k < 5 && i < len(b); k++ {
		c := b[i]
		i++
		if k == 0 && c == 0x80 || v > 0xFFFFFFFF>>7 {
			return 0, i, false
		}
		v = v<<7 | uint32(c&0x7f)
		if c&0x80 == 0 {
			return v, i, true
		}
	}
	return 0, i, false
}

// decodedBytes is what a WOFF or WOFF2 font weighs once decoded to an sfnt,
// from its table directory: the directory and the tables, each padded to four
// bytes. It is 0 for a plain font, which is charged as its part, and for a
// directory that is cut short.
func decodedBytes(b []byte) int64 {
	tabs, ok := woffTables(b)
	if !ok {
		return 0
	}
	total := int64(12 + 16*len(tabs))
	for _, t := range tabs {
		total += (t.orig + 3) &^ 3
	}
	return total
}

// shadeBytes is what the document holds for each shading a gradient creates,
// and shadeStopBytes for each stop of it: measured at about 4.8 KiB heap for
// a two-stop shading at 100,000 of them (485 MiB at the peak).
const (
	shadeBytes     = 5 << 10
	shadeStopBytes = 256
)
