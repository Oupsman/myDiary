package picture

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// testJPEG returns a w×h JPEG, red on its left half, with an EXIF
// orientation tag unless orientation is 0.
func testJPEG(t *testing.T, w, h, orientation int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{0, 0, 255, 255}
			if x < w/2 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if orientation == 0 {
		return data
	}
	// Big-endian TIFF with one IFD entry: orientation (SHORT).
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01")
	tiff = binary.BigEndian.AppendUint16(tiff, uint16(orientation))
	tiff = append(tiff, 0, 0, 0, 0, 0, 0)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := append([]byte{0xFF, 0xE1}, binary.BigEndian.AppendUint16(nil, uint16(len(seg)+2))...)
	app1 = append(app1, seg...)
	return append(append([]byte{0xFF, 0xD8}, app1...), data[2:]...)
}

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestShrinkJPEG(t *testing.T) {
	mime, out, ok := Shrink(testJPEG(t, 400, 200, 0), 100)
	if !ok || mime != "image/jpeg" {
		t.Fatalf("ok=%v mime=%q", ok, mime)
	}
	if b := decode(t, out).Bounds(); b.Dx() != 100 || b.Dy() != 50 {
		t.Fatalf("got %v, want 100×50", b)
	}
}

func TestShrinkOrientation(t *testing.T) {
	data := testJPEG(t, 400, 200, 6)
	if o := exifOrientation(data); o != 6 {
		t.Fatalf("orientation %d, want 6", o)
	}
	_, out, ok := Shrink(data, 100)
	if !ok {
		t.Fatal("not shrunk")
	}
	img := decode(t, out)
	if b := img.Bounds(); b.Dx() != 50 || b.Dy() != 100 {
		t.Fatalf("got %v, want 50×100 (turned)", b)
	}
	// Turned clockwise, the red left half ends up at the top.
	if r, _, bl, _ := img.At(25, 10).RGBA(); r < bl {
		t.Errorf("top is not red: r=%d b=%d", r, bl)
	}
}

func TestShrinkSmallUnchanged(t *testing.T) {
	data := testJPEG(t, 40, 20, 0)
	if _, out, ok := Shrink(data, 100); ok || !bytes.Equal(out, data) {
		t.Fatal("small picture was re-encoded")
	}
	if _, _, ok := Shrink([]byte("not a picture"), 100); ok {
		t.Fatal("garbage accepted")
	}
}

func TestShrinkPNGStaysPNG(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 300, 300)))
	mime, out, ok := Shrink(buf.Bytes(), 100)
	if !ok || mime != "image/png" || decode(t, out).Bounds().Dx() != 100 {
		t.Fatalf("ok=%v mime=%q", ok, mime)
	}
}
