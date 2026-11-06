package lumen

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	xdraw "golang.org/x/image/draw"
)

// ErrUnsupported marks a format Lumen cannot write.
type ErrUnsupported struct{ Format string }

func (e ErrUnsupported) Error() string { return "unsupported format: " + e.Format }

func decodeFile(path string) (image.Image, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	img, format, err := image.Decode(f)
	if err != nil {
		return nil, "", fmt.Errorf("decode %s: %w", path, err)
	}
	return img, format, nil
}

// normFormat maps names and extensions to "png" or "jpeg".
func normFormat(s string) (string, error) {
	switch strings.ToLower(strings.TrimPrefix(s, ".")) {
	case "png", "image/png":
		return "png", nil
	case "jpg", "jpeg", "image/jpeg":
		return "jpeg", nil
	case "avif", "image/avif":
		return "", ErrUnsupported{"avif (no pure-Go AVIF encoder available)"}
	}
	return "", ErrUnsupported{s}
}

func formatFromPath(path string) (string, error) {
	ext := filepath.Ext(path)
	if ext == "" {
		return "png", nil
	}
	return normFormat(ext)
}

func encode(w io.Writer, img image.Image, format string, quality int) error {
	switch format {
	case "png":
		return png.Encode(w, img)
	case "jpeg":
		if quality < 1 || quality > 100 {
			return fmt.Errorf("quality must be 1-100, got %d", quality)
		}
		return jpeg.Encode(w, img, &jpeg.Options{Quality: quality})
	}
	return ErrUnsupported{format}
}

// writeFile encodes to path, removing the partial file on failure.
func writeFile(path string, img image.Image, format string, quality int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = encode(f, img, format, quality)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

// scale resizes to w x h with Catmull-Rom.
func scale(src image.Image, w, h int) image.Image {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return dst
}

// targetSize resolves the output size from optional width/height.
func targetSize(sw, sh, w, h int) (int, int, error) {
	if w < 0 || h < 0 || (w == 0 && h == 0) {
		return 0, 0, fmt.Errorf("width and/or height must be positive")
	}
	if w > 20000 || h > 20000 {
		return 0, 0, fmt.Errorf("target size too large")
	}
	switch {
	case h == 0:
		h = max(1, (sh*w+sw/2)/sw)
	case w == 0:
		w = max(1, (sw*h+sh/2)/sh)
	}
	return w, h, nil
}

// Histogram holds 256-bin counts per 8-bit channel (non-premultiplied).
type Histogram struct {
	Red, Green, Blue, Alpha [256]int
}

func computeHistogram(img image.Image) Histogram {
	var h Histogram
	b := img.Bounds()
	n := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	xdraw.Draw(n, n.Bounds(), img, b.Min, xdraw.Src)
	for i := 0; i < len(n.Pix); i += 4 {
		h.Red[n.Pix[i]]++
		h.Green[n.Pix[i+1]]++
		h.Blue[n.Pix[i+2]]++
		h.Alpha[n.Pix[i+3]]++
	}
	return h
}
