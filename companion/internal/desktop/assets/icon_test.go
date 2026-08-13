package assets

import (
	"bytes"
	"image/png"
	"testing"
)

func TestIconIsEmbeddedCompatiblePNG(t *testing.T) {
	contents := IconPNG()
	if !bytes.HasPrefix(contents, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("IconPNG() omitted PNG signature")
	}
	image, err := png.Decode(bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("Decode(IconPNG()) error = %v", err)
	}
	if image.Bounds().Dx() != 22 || image.Bounds().Dy() != 22 {
		t.Fatalf("icon bounds = %v, want 22x22", image.Bounds())
	}
}
