// Command mediacheck checks media files with its own parsers, not ffmpeg's or
// ffprobe's, so demo.sh judges the wrapped tool's output independently.
// Standard library only. Every command exits non-zero when a file is
// malformed, internally inconsistent, or silent/blank.
//
//	mediacheck wav F          RIFF/WAVE: header fields, data length vs header, PCM stats
//	mediacheck tone F HZ      fraction of channel-0 energy at HZ (Goertzel); fails below 0.5
//	mediacheck pcmcmp A B     decode two WAVs, compare every sample
//	mediacheck container F    mp3 / ogg(vorbis) / flac / mp4 / png / jpeg structure
//	mediacheck imgdiff A B    number of differing pixels between two decoded images
//
// Output is one line of key=value pairs so shell can pick fields out.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"strconv"
)

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, "mediacheck: FAIL: "+f+"\n", a...); os.Exit(1) }

func read(p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		die("%v", err)
	}
	return b
}

func main() {
	a := os.Args
	if len(a) < 3 {
		die("usage: mediacheck wav|tone|pcmcmp|container|imgdiff FILE...")
	}
	switch a[1] {
	case "wav":
		w := parseWAV(read(a[2]))
		fmt.Printf("rate=%d channels=%d bits=%d frames=%d dur_ms=%d peak=%d rms=%.1f nonzero=%d of=%d\n",
			w.rate, w.channels, w.bits, w.frames, w.frames*1000/w.rate, w.peak(), w.rms(), w.nonzero(), len(w.s))
		if w.peak() == 0 {
			die("%s is digital silence (every sample is 0)", a[2])
		}
	case "tone":
		if len(a) < 4 {
			die("tone F HZ")
		}
		hz, _ := strconv.ParseFloat(a[3], 64)
		w := parseWAV(read(a[2]))
		r := w.toneRatio(hz)
		fmt.Printf("hz=%v ratio=%.4f\n", hz, r)
		if r < 0.5 {
			die("only %.1f%% of the energy is at %v Hz", r*100, hz)
		}
	case "pcmcmp":
		x, y := parseWAV(read(a[2])), parseWAV(read(a[3]))
		if x.rate != y.rate || x.channels != y.channels || len(x.s) != len(y.s) {
			die("shape differs: %d Hz %dch %d samples vs %d Hz %dch %d samples", x.rate, x.channels, len(x.s), y.rate, y.channels, len(y.s))
		}
		d, maxd := 0, 0
		for i := range x.s {
			if x.s[i] != y.s[i] {
				d++
				maxd = max(maxd, int(math.Abs(float64(x.s[i]-y.s[i]))))
			}
		}
		fmt.Printf("samples=%d differing=%d maxdiff=%d\n", len(x.s), d, maxd)
		if d != 0 {
			os.Exit(1)
		}
	case "container":
		container(read(a[2]))
	case "imgdiff":
		x, y := decode(read(a[2])), decode(read(a[3]))
		if x.Bounds() != y.Bounds() {
			die("sizes differ: %v vs %v", x.Bounds(), y.Bounds())
		}
		n := 0
		b := x.Bounds()
		for j := b.Min.Y; j < b.Max.Y; j++ {
			for i := b.Min.X; i < b.Max.X; i++ {
				r1, g1, b1, _ := x.At(i, j).RGBA()
				r2, g2, b2, _ := y.At(i, j).RGBA()
				if r1 != r2 || g1 != g2 || b1 != b2 {
					n++
				}
			}
		}
		fmt.Printf("pixels=%d differing=%d\n", b.Dx()*b.Dy(), n)
	default:
		die("unknown command %q", a[1])
	}
}

// ---- WAV ----

type wav struct {
	rate, channels, bits, frames int
	s                            []int32 // interleaved, scaled to the file's own bit depth
}

func (w *wav) peak() int {
	p := 0
	for _, v := range w.s {
		p = max(p, int(math.Abs(float64(v))))
	}
	return p
}
func (w *wav) rms() float64 {
	var t float64
	for _, v := range w.s {
		t += float64(v) * float64(v)
	}
	return math.Sqrt(t / float64(len(w.s)))
}
func (w *wav) nonzero() int {
	n := 0
	for _, v := range w.s {
		if v != 0 {
			n++
		}
	}
	return n
}

// toneRatio is the share of channel-0 energy at hz: Goertzel power over the
// total energy (Parseval). A pure tone scores ~1, noise or silence ~0.
func (w *wav) toneRatio(hz float64) float64 {
	n := w.frames
	k := 2 * math.Cos(2*math.Pi*hz/float64(w.rate))
	var s1, s2, e float64
	for i := 0; i < n; i++ {
		x := float64(w.s[i*w.channels])
		s0 := x + k*s1 - s2
		s2, s1 = s1, s0
		e += x * x
	}
	if e == 0 {
		return 0
	}
	p := s1*s1 + s2*s2 - k*s1*s2
	return (2 * p / float64(n)) / e // a sine of amplitude A: power A^2 n^2/4, energy A^2 n/2
}

func parseWAV(b []byte) *wav {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		die("not RIFF/WAVE")
	}
	if got := int(binary.LittleEndian.Uint32(b[4:8])); got != len(b)-8 {
		die("RIFF size field %d but the file is %d bytes (%d expected): truncated or padded", got, len(b), len(b)-8)
	}
	var w wav
	var data []byte
	haveFmt, haveData := false, false
	var tag, block int
	for p := 12; p+8 <= len(b); {
		id, n := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:p+8]))
		body := p + 8
		if body+n > len(b) {
			die("chunk %q claims %d bytes but only %d remain: truncated", id, n, len(b)-body)
		}
		switch id {
		case "fmt ":
			if n < 16 {
				die("fmt chunk is %d bytes", n)
			}
			tag = int(binary.LittleEndian.Uint16(b[body:]))
			w.channels = int(binary.LittleEndian.Uint16(b[body+2:]))
			w.rate = int(binary.LittleEndian.Uint32(b[body+4:]))
			byteRate := int(binary.LittleEndian.Uint32(b[body+8:]))
			block = int(binary.LittleEndian.Uint16(b[body+12:]))
			w.bits = int(binary.LittleEndian.Uint16(b[body+14:]))
			if tag == 0xFFFE && n >= 26 {
				tag = int(binary.LittleEndian.Uint16(b[body+24:])) // sub-format GUID starts with the tag
			}
			if tag != 1 {
				die("format tag %#x: only integer PCM is checked", tag)
			}
			if w.channels < 1 || w.rate < 1 || w.bits%8 != 0 || w.bits < 8 || w.bits > 32 {
				die("implausible fmt: %d ch, %d Hz, %d bit", w.channels, w.rate, w.bits)
			}
			if block != w.channels*w.bits/8 {
				die("block align %d != channels*bits/8 = %d", block, w.channels*w.bits/8)
			}
			if byteRate != w.rate*block {
				die("byte rate %d != rate*block = %d", byteRate, w.rate*block)
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				die("data chunk before fmt chunk")
			}
			data, haveData = b[body:body+n], true
		}
		p = body + n + n&1
	}
	if !haveFmt || !haveData {
		die("missing fmt or data chunk")
	}
	if len(data)%block != 0 {
		die("data length %d is not a whole number of %d-byte frames", len(data), block)
	}
	w.frames = len(data) / block
	if w.frames == 0 {
		die("data chunk is empty")
	}
	bytesPer := w.bits / 8
	w.s = make([]int32, len(data)/bytesPer)
	for i := range w.s {
		o := i * bytesPer
		switch bytesPer {
		case 1:
			w.s[i] = int32(data[o]) - 128 // 8-bit WAV is unsigned
		case 2:
			w.s[i] = int32(int16(binary.LittleEndian.Uint16(data[o:])))
		case 3:
			w.s[i] = int32(uint32(data[o])<<8|uint32(data[o+1])<<16|uint32(data[o+2])<<24) >> 8
		case 4:
			w.s[i] = int32(binary.LittleEndian.Uint32(data[o:]))
		}
	}
	return &w
}

// ---- other containers ----

func decode(b []byte) image.Image {
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		die("image does not decode: %v", err)
	}
	return img
}

func container(b []byte) {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG")), bytes.HasPrefix(b, []byte("\xff\xd8")):
		img := decode(b)
		bb := img.Bounds()
		seen := map[[3]uint32]bool{}
		var sum float64
		for y := bb.Min.Y; y < bb.Max.Y; y++ {
			for x := bb.Min.X; x < bb.Max.X; x++ {
				r, g, bl, _ := img.At(x, y).RGBA()
				sum += float64(r+g+bl) / 3 / 257
				seen[[3]uint32{r >> 8, g >> 8, bl >> 8}] = true
			}
		}
		kind := "png"
		if b[0] == 0xff {
			kind = "jpeg"
		}
		fmt.Printf("%s width=%d height=%d mean=%.1f colors=%d\n", kind, bb.Dx(), bb.Dy(), sum/float64(bb.Dx()*bb.Dy()), len(seen))
		if len(seen) < 4 {
			die("image is blank (%d distinct colours)", len(seen))
		}
	case bytes.HasPrefix(b, []byte("fLaC")):
		flac(b)
	case bytes.HasPrefix(b, []byte("OggS")):
		ogg(b)
	case len(b) > 8 && string(b[4:8]) == "ftyp":
		mp4(b)
	default:
		mp3(b)
	}
}

func flac(b []byte) {
	if len(b) < 42 || b[4]&0x7f != 0 || b[5] != 0 || b[6] != 0 || b[7] != 34 {
		die("flac: first metadata block is not a 34-byte STREAMINFO")
	}
	si := b[8:42]
	v := binary.BigEndian.Uint64(si[10:18]) // rate(20) ch(3) bps(5) total(36)
	rate, ch, bps, total := int(v>>44), int(v>>41&7)+1, int(v>>36&31)+1, int64(v&(1<<36-1))
	if rate == 0 || total == 0 {
		die("flac: STREAMINFO rate=%d total_samples=%d", rate, total)
	}
	// the audio frames after the metadata must start with the 14-bit sync 0x3FFE
	p := 4
	for {
		last := b[p]&0x80 != 0
		n := int(b[p+1])<<16 | int(b[p+2])<<8 | int(b[p+3])
		p += 4 + n
		if last {
			break
		}
		if p+4 > len(b) {
			die("flac: metadata runs off the end")
		}
	}
	if p+2 > len(b) || b[p] != 0xff || b[p+1]&0xfc != 0xf8 {
		die("flac: no frame sync after the metadata")
	}
	fmt.Printf("flac rate=%d channels=%d bits=%d samples=%d dur_ms=%d\n", rate, ch, bps, total, total*1000/int64(rate))
}

func ogg(b []byte) {
	var crcTab [256]uint32
	for i := range crcTab {
		c := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
		crcTab[i] = c
	}
	pages, rate, ch := 0, 0, 0
	var last int64
	eos := false
	for p := 0; p < len(b); {
		if p+27 > len(b) || string(b[p:p+4]) != "OggS" {
			die("ogg: no page at offset %d", p)
		}
		ns := int(b[p+26])
		hl := 27 + ns
		if p+hl > len(b) {
			die("ogg: truncated page header")
		}
		pl := 0
		for _, l := range b[p+27 : p+hl] {
			pl += int(l)
		}
		if p+hl+pl > len(b) {
			die("ogg: page at %d wants %d bytes, %d remain: truncated", p, hl+pl, len(b)-p)
		}
		pg := append([]byte(nil), b[p:p+hl+pl]...)
		want := binary.LittleEndian.Uint32(pg[22:])
		copy(pg[22:26], []byte{0, 0, 0, 0})
		var c uint32
		for _, x := range pg {
			c = c<<8 ^ crcTab[byte(c>>24)^x]
		}
		if c != want {
			die("ogg: page %d CRC %#x != computed %#x", pages, want, c)
		}
		if pages == 0 {
			body := pg[hl:]
			if len(body) < 16 || string(body[:7]) != "\x01vorbis" {
				die("ogg: first packet is not a Vorbis identification header")
			}
			ch, rate = int(body[11]), int(binary.LittleEndian.Uint32(body[12:]))
		}
		if g := int64(binary.LittleEndian.Uint64(pg[6:])); g >= 0 {
			last = g
		}
		eos = pg[5]&4 != 0
		pages++
		p += hl + pl
	}
	if !eos {
		die("ogg: last page lacks the end-of-stream flag: truncated")
	}
	if rate == 0 || last == 0 {
		die("ogg: rate=%d final granule=%d", rate, last)
	}
	fmt.Printf("ogg-vorbis rate=%d channels=%d pages=%d granule=%d dur_ms=%d crc=ok\n", rate, ch, pages, last, last*1000/int64(rate))
}

func mp3(b []byte) {
	p, end := 0, len(b)
	if bytes.HasPrefix(b, []byte("ID3")) && len(b) > 10 {
		p = 10 + (int(b[6]&0x7f)<<21 | int(b[7]&0x7f)<<14 | int(b[8]&0x7f)<<7 | int(b[9]&0x7f))
	}
	if end >= 128 && string(b[end-128:end-125]) == "TAG" {
		end -= 128
	}
	br1 := []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	br2 := []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
	frames, rate, ch, spf, info := 0, 0, 0, 0, false
	for p < end {
		if p+4 > end || b[p] != 0xff || b[p+1]&0xe0 != 0xe0 {
			die("mp3: no frame sync at offset %d after %d good frames", p, frames)
		}
		ver, layer := b[p+1]>>3&3, b[p+1]>>1&3
		bri, sri, pad := int(b[p+2]>>4), int(b[p+2]>>2&3), int(b[p+2]>>1&1)
		if ver == 1 || layer != 1 || bri == 0 || bri == 15 || sri == 3 {
			die("mp3: unsupported or invalid header %x at offset %d (only layer III checked)", b[p:p+4], p)
		}
		base := []int{44100, 48000, 32000}[sri]
		var kbps, fl int
		if ver == 3 {
			kbps, spf = br1[bri], 1152
			fl = 144*kbps*1000/base + pad
		} else {
			base >>= 1
			if ver == 0 {
				base >>= 1
			}
			kbps, spf = br2[bri], 576
			fl = 72*kbps*1000/base + pad
		}
		if frames == 0 {
			rate, ch = base, 2
			if b[p+3]>>6 == 3 {
				ch = 1
			}
			if t := b[p+4 : min(p+4+40, end)]; bytes.Contains(t, []byte("Xing")) || bytes.Contains(t, []byte("Info")) {
				info = true
			}
		}
		frames++
		p += fl
	}
	if p != end {
		die("mp3: last frame runs %d bytes past the end: truncated", p-end)
	}
	if frames == 0 {
		die("not a recognised container (no PNG/JPEG/FLAC/Ogg/MP4 magic, no MP3 frames)")
	}
	audio := frames
	if info {
		audio-- // the Xing/Info frame carries no audio
	}
	fmt.Printf("mp3 rate=%d channels=%d frames=%d audio_frames=%d dur_ms=%d\n", rate, ch, frames, audio, audio*spf*1000/rate)
}

// mp4 walks the box tree: top-level boxes must tile the file exactly; the
// movie header gives duration, each track header its size, and the video
// track's stts table the exact number of frames.
func mp4(b []byte) {
	box := func(d []byte, f func(kind string, body []byte)) {
		for p := 0; p < len(d); {
			if p+8 > len(d) {
				die("mp4: truncated box header")
			}
			n, hdr := int(binary.BigEndian.Uint32(d[p:])), 8
			if n == 1 {
				n, hdr = int(binary.BigEndian.Uint64(d[p+8:])), 16
			}
			if n < hdr || p+n > len(d) {
				die("mp4: box %q at %d claims %d bytes, %d remain: truncated", d[p+4:p+8], p, n, len(d)-p)
			}
			f(string(d[p+4:p+8]), d[p+hdr:p+n])
			p += n
		}
	}
	var brand string
	var haveMoov, haveMdat bool
	var durMs int64
	var tracks, videoFrames, w, h int
	var vtracks, atracks int
	order := []string{}
	box(b, func(k string, body []byte) {
		order = append(order, k)
		switch k {
		case "ftyp":
			brand = string(body[:4])
		case "mdat":
			haveMdat = len(body) > 0
		case "moov":
			haveMoov = true
			box(body, func(k string, body []byte) {
				switch k {
				case "mvhd":
					if body[0] == 1 {
						durMs = int64(binary.BigEndian.Uint64(body[24:])) * 1000 / int64(binary.BigEndian.Uint32(body[20:]))
					} else {
						durMs = int64(binary.BigEndian.Uint32(body[16:])) * 1000 / int64(binary.BigEndian.Uint32(body[12:]))
					}
				case "trak":
					tracks++
					kind := ""
					box(body, func(k string, body []byte) {
						switch k {
						case "tkhd":
							o := 76
							if body[0] == 1 {
								o = 88
							}
							tw, th := int(binary.BigEndian.Uint32(body[o:])>>16), int(binary.BigEndian.Uint32(body[o+4:])>>16)
							if tw > 0 {
								w, h = tw, th
							}
						case "mdia":
							box(body, func(k string, body []byte) {
								switch k {
								case "hdlr":
									kind = string(body[8:12])
								case "minf":
									box(body, func(k string, body []byte) {
										if k != "stbl" {
											return
										}
										box(body, func(k string, body []byte) {
											if k == "stts" && kind == "vide" {
												for i := 0; i < int(binary.BigEndian.Uint32(body[4:])); i++ {
													videoFrames += int(binary.BigEndian.Uint32(body[8+8*i:]))
												}
											}
										})
									})
								}
							})
						}
					})
					switch kind {
					case "vide":
						vtracks++
					case "soun":
						atracks++
					}
				}
			})
		}
	})
	if order[0] != "ftyp" || !haveMoov || !haveMdat {
		die("mp4: need ftyp first, a moov and a non-empty mdat; got %v", order)
	}
	fmt.Printf("mp4 brand=%s boxes=%v dur_ms=%d video_tracks=%d audio_tracks=%d width=%d height=%d video_frames=%d\n",
		brand, order, durMs, vtracks, atracks, w, h, videoFrames)
	if vtracks > 0 && videoFrames == 0 {
		die("mp4: video track has no samples")
	}
}
