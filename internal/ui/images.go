package ui

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gdkpixbuf/v2"
)

// maxImageWidth is the width, in pixels, pictures are scaled down to when
// shown in an entry. The original file is kept untouched in the diary.
const maxImageWidth = 560

// maxImageFileSize guards against accidentally embedding huge files.
const maxImageFileSize = 50 << 20

func readImageFile(path string) (mime string, data []byte, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if st.Size() > maxImageFileSize {
		return "", nil, errors.New("picture is larger than 50 MiB")
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	mime = http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		// Let the pixbuf loader decide (e.g. HEIF, TIFF variants).
		mime = "application/octet-stream"
	}
	return mime, data, nil
}

// displayTexture decodes an image, applies its EXIF orientation and scales it
// down for display.
func displayTexture(data []byte) (*gdk.Texture, error) {
	loader := gdkpixbuf.NewPixbufLoader()
	if err := loader.Write(data); err != nil {
		loader.Close()
		return nil, err
	}
	if err := loader.Close(); err != nil {
		return nil, err
	}
	pb := loader.Pixbuf()
	if pb == nil {
		return nil, errors.New("unsupported picture format")
	}
	if o := pb.ApplyEmbeddedOrientation(); o != nil {
		pb = o
	}
	if w := pb.Width(); w > maxImageWidth {
		h := max(1, pb.Height()*maxImageWidth/w)
		pb = pb.ScaleSimple(maxImageWidth, h, gdkpixbuf.InterpBilinear)
	}
	return gdk.NewTextureForPixbuf(pb), nil
}
