package launcher

import (
	"os"
	"path/filepath"
	"testing"
)

// apps/ffmpeg's verbs were unreachable through `aurora open` because this
// table knew images only. FLAC is the case that matters: Go does not sniff it,
// so without an entry it stays application/octet-stream and matches no verb.
func TestDetectMIMEAudioAndVideo(t *testing.T) {
	dir := t.TempDir()
	// Real magic bytes, padded so the sniffer sees a full buffer.
	for _, c := range []struct {
		name, want string
		head       []byte
	}{
		{"a.flac", "audio/flac", []byte("fLaC\x00\x00\x00\x22")},
		{"a.wav", "audio/wave", []byte("RIFF\x24\x08\x00\x00WAVEfmt ")},
		{"a.mp3", "audio/mpeg", []byte("ID3\x03\x00\x00\x00\x00\x00\x00")},
		{"a.ogg", "application/ogg", []byte("OggS\x00\x02\x00\x00")},
		{"a.mp4", "video/mp4", []byte("\x00\x00\x00\x20ftypisom\x00\x00\x02\x00isomiso2")},
	} {
		p := filepath.Join(dir, c.name)
		if err := os.WriteFile(p, append(c.head, make([]byte, 600)...), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := DetectMIME(p)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// An extension alone must still answer, for a file whose bytes say nothing.
func TestDetectMIMEFlacByExtensionAlone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mystery.flac")
	if err := os.WriteFile(p, make([]byte, 600), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := DetectMIME(p); got != "audio/flac" {
		t.Errorf("got %q, want audio/flac from the extension", got)
	}
}
