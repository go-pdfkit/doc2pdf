package xps

import (
	"errors"
	"testing"

	"github.com/go-pdfkit/pdfkit"
)

// A JPEG pdfkit refuses is an ErrBadImage, and the refusal is remembered: the
// draws after the first neither embed nor charge the bytes again, so a
// thousand draws of it cannot end the conversion by the embedded-bytes limit.
func TestSecRefusedJPEGIsRememberedNotRetried(t *testing.T) {
	data := []byte{0xFF, 0xD8, 0xFF, 0xD9} // a start and an end of image, no frame
	c := newConv(t, newPkg(page(``)).bytes(t), Limits{MaxEmbeddedBytes: int64(2*len(data) + 1)})
	r := &renderer{c: c, p: c.doc.AddPage(pdfkit.NewPageSize(100, 100))}
	im := &imageInfo{data: data, kind: "jpeg", w: 8, h: 8}
	for i := 0; i < 5; i++ {
		h, err := r.register(im)
		if !errors.Is(err, ErrBadImage) || h != nil {
			t.Fatalf("draw %d: handle %v, err %v, want ErrBadImage", i, h, err)
		}
	}
	if c.embedded != int64(len(data)) {
		t.Errorf("embedded %d bytes after five draws, want %d: the refusal was tried again", c.embedded, len(data))
	}
}
