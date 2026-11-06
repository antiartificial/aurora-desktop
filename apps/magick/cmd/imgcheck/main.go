// Command imgcheck decodes image files with Go's own decoders (not
// ImageMagick), so demo.sh checks the wrapped tool's output independently.
//
//	imgcheck info F        -> "FORMAT W H" (format as Go's decoder names it)
//	imgcheck px F X Y      -> "R G B" 8-bit at that pixel
//	imgcheck gray F        -> max |R-G|,|G-B| over all pixels (0 = pure gray)
//	imgcheck frames F      -> frame count (gif)
package main

import (
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strconv"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, "imgcheck: "+f+"\n", a...); os.Exit(1) }

func load(p string) (image.Image, string) {
	f, err := os.Open(p)
	if err != nil {
		die("%v", err)
	}
	defer f.Close()
	img, fmtName, err := image.Decode(f)
	if err != nil {
		die("%s: %v", p, err)
	}
	return img, fmtName
}

func rgb8(img image.Image, x, y int) (int, int, int) {
	r, g, b, _ := img.At(x, y).RGBA()
	return int(r >> 8), int(g >> 8), int(b >> 8)
}

func main() {
	if len(os.Args) < 3 {
		die("usage: imgcheck info|px|gray|frames FILE [X Y]")
	}
	cmd, path := os.Args[1], os.Args[2]
	switch cmd {
	case "frames":
		f, err := os.Open(path)
		if err != nil {
			die("%v", err)
		}
		g, err := gif.DecodeAll(f)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(len(g.Image))
		return
	}
	img, name := load(path)
	b := img.Bounds()
	switch cmd {
	case "info":
		fmt.Println(name, b.Dx(), b.Dy())
	case "px":
		x, _ := strconv.Atoi(os.Args[3])
		y, _ := strconv.Atoi(os.Args[4])
		r, g, bl := rgb8(img, b.Min.X+x, b.Min.Y+y)
		fmt.Println(r, g, bl)
	case "gray":
		m := 0
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl := rgb8(img, x, y)
				for _, d := range []int{r - g, g - bl} {
					if d < 0 {
						d = -d
					}
					m = max(m, d)
				}
			}
		}
		fmt.Println(m)
	default:
		die("unknown command %q", cmd)
	}
}
