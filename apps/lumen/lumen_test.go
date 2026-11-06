package lumen

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// gradient makes a w x h image with a horizontal gradient.
func gradient(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255})
		}
	}
	return img
}

// halfRedBlue: left half pure red, right half pure blue.
func halfRedBlue(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{255, 0, 0, 255}
			if x >= w/2 {
				c = color.NRGBA{0, 0, 255, 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func decode(t *testing.T, path string) (image.Image, string) {
	t.Helper()
	img, f, err := decodeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return img, f
}

func call(t *testing.T, verb string, req any) (map[string]any, int) {
	t.Helper()
	in, _ := json.Marshal(req)
	var out, errb bytes.Buffer
	code := Run([]string{"--verb", verb}, bytes.NewReader(in), &out, &errb)
	var res map[string]any
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("stdout not JSON: %q", out.String())
	}
	return res, code
}

func TestResizeWidthKeepsAspect(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.png"), filepath.Join(dir, "out.png")
	writePNG(t, in, gradient(400, 300))
	if code := Run([]string{"resize", "--input", in, "--output", out, "--width", "200"}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("exit", code)
	}
	img, f := decode(t, out)
	if f != "png" || img.Bounds().Dx() != 200 || img.Bounds().Dy() != 150 {
		t.Fatalf("got %s %v", f, img.Bounds())
	}
	// Smoothness: a catmull-rom downscale of a gradient must not be blocky.
	r0, _, _, _ := img.At(100, 75).RGBA()
	if r0>>8 < 120 || r0>>8 > 136 {
		t.Fatalf("mid red %d, expected ~127", r0>>8)
	}
}

func TestResizeExplicitAndUpscale(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.png"), filepath.Join(dir, "out.png")
	writePNG(t, in, gradient(100, 50))
	if _, code := call(t, "resize@v1", Request{Input: in, Output: out, Args: args(300, 70, "", 0)}); code != 0 {
		t.Fatal("exit", code)
	}
	if img, _ := decode(t, out); img.Bounds().Dx() != 300 || img.Bounds().Dy() != 70 {
		t.Fatal(img.Bounds())
	}
}

func args(w, h int, f string, q int) (a struct {
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Format  string `json:"format"`
	Quality int    `json:"quality"`
}) {
	a.Width, a.Height, a.Format, a.Quality = w, h, f, q
	return
}

func TestExportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p, j, p2 := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.jpg"), filepath.Join(dir, "c.png")
	writePNG(t, p, gradient(64, 48))
	res, code := call(t, "export@v1", Request{Input: p, Output: j, Args: args(0, 0, "jpeg", 80)})
	if code != 0 || res["ok"] != true {
		t.Fatal(res)
	}
	if ev := res["event"].(map[string]any); ev["name"] != "export.finished" {
		t.Fatal(ev)
	}
	if _, f := decode(t, j); f != "jpeg" {
		t.Fatal(f)
	}
	if _, code := call(t, "export@v1", Request{Input: j, Output: p2, Args: args(0, 0, "png", 0)}); code != 0 {
		t.Fatal("jpeg->png failed")
	}
	if img, f := decode(t, p2); f != "png" || img.Bounds().Dx() != 64 {
		t.Fatal(f)
	}
}

func TestJPEGQualityMatters(t *testing.T) {
	dir := t.TempDir()
	p, lo, hi := filepath.Join(dir, "a.png"), filepath.Join(dir, "lo.jpg"), filepath.Join(dir, "hi.jpg")
	writePNG(t, p, gradient(128, 128))
	call(t, "export@v1", Request{Input: p, Output: lo, Args: args(0, 0, "jpeg", 10)})
	call(t, "export@v1", Request{Input: p, Output: hi, Args: args(0, 0, "jpeg", 95)})
	a, _ := os.Stat(lo)
	b, _ := os.Stat(hi)
	if a.Size() >= b.Size() {
		t.Fatalf("q10=%d q95=%d", a.Size(), b.Size())
	}
}

func TestExportUnsupported(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.png")
	writePNG(t, p, gradient(8, 8))
	for _, f := range []string{"avif", "webp"} {
		out := filepath.Join(dir, "o."+f)
		res, code := call(t, "export@v1", Request{Input: p, Output: out, Args: args(0, 0, f, 0)})
		if code == 0 || res["ok"] != false || !strings.Contains(res["error"].(string), "unsupported format") {
			t.Fatalf("%s: %v", f, res)
		}
		if _, err := os.Stat(out); err == nil {
			t.Fatal("output written for unsupported format")
		}
	}
}

func TestHistogramExact(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rb.png")
	writePNG(t, p, halfRedBlue(10, 4)) // 20 red px, 20 blue px
	res, code := call(t, "histogram@v1", Request{Input: p})
	if code != 0 {
		t.Fatal(res)
	}
	get := func(ch string, i int) int { return int(res[ch].([]any)[i].(float64)) }
	want := map[string][2]int{"red": {20, 20}, "green": {40, 0}, "blue": {20, 20}} // {bin0, bin255}
	for ch, w := range want {
		if get(ch, 0) != w[0] || get(ch, 255) != w[1] {
			t.Errorf("%s: bin0=%d bin255=%d want %v", ch, get(ch, 0), get(ch, 255), w)
		}
		sum := 0
		for _, v := range res[ch].([]any) {
			sum += int(v.(float64))
		}
		if sum != 40 {
			t.Errorf("%s sums to %d", ch, sum)
		}
	}
}

func TestHistogramWritesNothing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.png")
	writePNG(t, p, gradient(16, 16))
	before, _ := os.ReadDir(dir)
	call(t, "histogram@v1", Request{Input: p, Output: filepath.Join(dir, "should-not-exist.json")})
	after, _ := os.ReadDir(dir)
	if len(before) != len(after) {
		t.Fatalf("directory changed: %d -> %d entries", len(before), len(after))
	}
}

func TestMissingInput(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{"resize@v1", "export@v1", "histogram@v1"} {
		res, code := call(t, v, Request{Input: filepath.Join(dir, "nope.png"), Output: filepath.Join(dir, "o.png"), Args: args(10, 0, "png", 0)})
		if code == 0 || res["ok"] != false || res["error"] == "" {
			t.Fatalf("%s: code=%d %v", v, code, res)
		}
	}
}

func TestBadUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if Run([]string{"frobnicate"}, nil, &out, &errb) == 0 || !strings.Contains(out.String(), `"ok":false`) {
		t.Fatal(out.String())
	}
	out.Reset()
	if Run([]string{"--verb", "resize@v1"}, strings.NewReader("{not json"), &out, &errb) == 0 {
		t.Fatal("bad JSON accepted")
	}
}
