// Command genimage writes a synthetic test PNG (for demo.sh).
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: genimage out.png")
		os.Exit(2)
	}
	const w, h = 1200, 800
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x + y) * 255 / (w + h)), 255}
			if (x/100+y/100)%2 == 0 {
				c.R, c.G = 255-c.R, 255-c.G
			}
			img.SetNRGBA(x, y, c)
		}
	}
	f, err := os.Create(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
