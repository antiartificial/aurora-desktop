package manifest

import (
	"reflect"
	"testing"
)

func names(vs []Verb) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Name)
	}
	return out
}

func TestVerbsAcceptingLumen(t *testing.T) {
	m := mustLoad(t, string(example(t, "lumen")))
	want := []string{"resize@v1", "export@v1", "histogram@v1"}
	if got := names(m.VerbsAccepting("image/png")); !reflect.DeepEqual(got, want) {
		t.Errorf("image/png: got %v want %v", got, want)
	}
	if got := names(m.VerbsAccepting("image/jpeg")); !reflect.DeepEqual(got, want) {
		t.Errorf("image/jpeg: got %v want %v", got, want)
	}
	for _, mime := range []string{"image/gif", "video/mp4", "text/csv"} {
		if got := m.VerbsAccepting(mime); len(got) != 0 {
			t.Errorf("%s: want none, got %v", mime, names(got))
		}
	}
}

func TestVerbsAcceptingWildcards(t *testing.T) {
	ff := mustLoad(t, string(example(t, "ffmpeg")))
	if got := names(ff.VerbsAccepting("video/mp4")); !reflect.DeepEqual(got, []string{"transcode@v1", "thumbnail@v1"}) {
		t.Errorf("video/mp4: %v", got)
	}
	if got := names(ff.VerbsAccepting("audio/flac")); !reflect.DeepEqual(got, []string{"transcode@v1"}) {
		t.Errorf("audio/flac: %v", got)
	}
	im := mustLoad(t, string(example(t, "imagemagick")))
	if got := names(im.VerbsAccepting("image/png")); !reflect.DeepEqual(got, []string{"convert@v1"}) {
		t.Errorf("imagemagick png: %v", got)
	}
	// "image/*" must not match a type that merely shares a prefix.
	if got := im.VerbsAccepting("imagex/png"); len(got) != 0 {
		t.Errorf("imagex/png matched %v", names(got))
	}
}
