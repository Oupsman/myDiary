// Package picture shrinks photos before they are stored in a diary, so that a
// few camera pictures do not make it hundreds of megabytes.
package picture

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
)

// MaxSide is the longest side, in pixels, pictures are shrunk to.
const MaxSide = 2048

const jpegQuality = 85

// Shrink scales a JPEG or PNG picture down so that its longest side is at
// most maxSide, applying the JPEG EXIF orientation. JPEGs are re-encoded,
// which also drops their metadata (GPS position included); PNGs stay PNGs.
// Other formats, and pictures that are already small enough, are returned
// unchanged with ok false.
func Shrink(data []byte, maxSide int) (mime string, out []byte, ok bool) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		return "", data, false
	}
	orientation := 1
	if format == "jpeg" {
		orientation = exifOrientation(data)
	}
	if max(cfg.Width, cfg.Height) <= maxSide && orientation == 1 {
		return "", data, false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", data, false
	}
	small := orient(scaleDown(img, maxSide), orientation)

	var buf bytes.Buffer
	if format == "png" {
		err = png.Encode(&buf, small)
		mime = "image/png"
	} else {
		err = jpeg.Encode(&buf, small, &jpeg.Options{Quality: jpegQuality})
		mime = "image/jpeg"
	}
	if err != nil {
		return "", data, false
	}
	return mime, buf.Bytes(), true
}

// scaleDown returns img as RGBA, with its longest side at most maxSide. Each
// output pixel is the average of the source pixels it covers.
func scaleDown(img image.Image, maxSide int) *image.RGBA {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	src := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	if max(sw, sh) <= maxSide {
		return src
	}
	dw, dh := maxSide, max(1, sh*maxSide/sw)
	if sh > sw {
		dw, dh = max(1, sw*maxSide/sh), maxSide
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		y0, y1 := y*sh/dh, max((y+1)*sh/dh, y*sh/dh+1)
		for x := range dw {
			x0, x1 := x*sw/dw, max((x+1)*sw/dw, x*sw/dw+1)
			var r, g, bl, a, n uint32
			for sy := y0; sy < y1; sy++ {
				row := src.Pix[sy*src.Stride:]
				for sx := x0; sx < x1; sx++ {
					p := row[sx*4 : sx*4+4]
					r += uint32(p[0])
					g += uint32(p[1])
					bl += uint32(p[2])
					a += uint32(p[3])
					n++
				}
			}
			o := dst.Pix[y*dst.Stride+x*4:]
			o[0], o[1], o[2], o[3] = uint8(r/n), uint8(g/n), uint8(bl/n), uint8(a/n)
		}
	}
	return dst
}

// orient turns an image the right way up given its EXIF orientation (1-8).
func orient(src *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			var sx, sy int
			switch o {
			case 2: // mirrored
				sx, sy = w-1-x, y
			case 3: // upside down
				sx, sy = w-1-x, h-1-y
			case 4: // mirrored upside down
				sx, sy = x, h-1-y
			case 5: // transposed
				sx, sy = y, x
			case 6: // turned left: rotate clockwise
				sx, sy = y, h-1-x
			case 7: // transversed
				sx, sy = w-1-y, h-1-x
			case 8: // turned right: rotate counter-clockwise
				sx, sy = w-1-y, x
			}
			copy(dst.Pix[y*dst.Stride+x*4:y*dst.Stride+x*4+4], src.Pix[sy*src.Stride+sx*4:])
		}
	}
	return dst
}

// exifOrientation returns the orientation tag of a JPEG, or 1.
func exifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for p := 2; p+4 <= len(data) && data[p] == 0xFF; {
		marker := data[p+1]
		size := int(binary.BigEndian.Uint16(data[p+2:]))
		if marker == 0xDA || size < 2 || p+2+size > len(data) {
			return 1 // start of the image data: no EXIF before it
		}
		seg := data[p+4 : p+2+size]
		if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return tiffOrientation(seg[6:])
		}
		p += 2 + size
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[ifd:]))
	for i := range n {
		e := ifd + 2 + i*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if o := int(bo.Uint16(t[e+8:])); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}
