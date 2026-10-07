package media

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func solid(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func decode(t *testing.T, b []byte) (image.Image, string) {
	t.Helper()
	img, f, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img, f
}

func TestProcessScalesAndReencodes(t *testing.T) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, solid(3200, 1600, color.RGBA{200, 30, 30, 255}), nil); err != nil {
		t.Fatal(err)
	}
	p, err := Process(&b)
	if err != nil {
		t.Fatal(err)
	}
	full, f1 := decode(t, p.Full)
	thumb, f2 := decode(t, p.Thumb)
	if f1 != "jpeg" || f2 != "jpeg" || p.Ext != "jpg" {
		t.Fatalf("formats %s/%s ext %s, want jpeg", f1, f2, p.Ext)
	}
	if fb := full.Bounds(); fb.Dx() != 1600 || fb.Dy() != 800 {
		t.Errorf("full = %v, want 1600x800", fb)
	}
	if tb := thumb.Bounds(); tb.Dx() != 600 || tb.Dy() != 300 {
		t.Errorf("thumb = %v, want 600x300", tb)
	}
	if len(p.Hash) != 20 {
		t.Errorf("hash %q", p.Hash)
	}
}

func TestSmallImagesAreNotUpscaled(t *testing.T) {
	p, err := Process(bytes.NewReader(encodePNG(t, solid(100, 50, color.RGBA{1, 2, 3, 255}))))
	if err != nil {
		t.Fatal(err)
	}
	if img, _ := decode(t, p.Full); img.Bounds().Dx() != 100 {
		t.Fatalf("a 100px image was resized to %v", img.Bounds())
	}
}

func TestTransparencyKeepsPNGOthersBecomeJPEG(t *testing.T) {
	clear := image.NewRGBA(image.Rect(0, 0, 10, 10))
	clear.Set(1, 1, color.NRGBA{255, 0, 0, 128})
	p, err := Process(bytes.NewReader(encodePNG(t, clear)))
	if err != nil {
		t.Fatal(err)
	}
	if _, f := decode(t, p.Full); f != "png" || p.Ext != "png" {
		t.Fatalf("transparent PNG became %s", f)
	}
	opaque, err := Process(bytes.NewReader(encodePNG(t, solid(10, 10, color.RGBA{9, 9, 9, 255}))))
	if err != nil {
		t.Fatal(err)
	}
	if opaque.Ext != "jpg" {
		t.Fatalf("opaque PNG ext = %s, want jpg", opaque.Ext)
	}
}

func TestGIFIsAccepted(t *testing.T) {
	var b bytes.Buffer
	g := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.White, color.Black})
	if err := gif.Encode(&b, g, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Process(&b); err != nil {
		t.Fatalf("gif rejected: %v", err)
	}
}

func TestRejectsNonImagesAndHostileInput(t *testing.T) {
	cases := map[string][]byte{
		"empty":             {},
		"text":              []byte("hello"),
		"svg":               []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html":              []byte("<html><script>alert(1)</script></html>"),
		"truncated":         encodePNG(t, solid(50, 50, color.White))[:40],
		"png header + junk": append(encodePNG(t, solid(5, 5, color.White))[:30], []byte("garbage")...),
	}
	for name, data := range cases {
		if _, err := Process(bytes.NewReader(data)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestRejectsOversizedUploadsAndPixelBombs(t *testing.T) {
	if _, err := Process(bytes.NewReader(make([]byte, MaxUploadBytes+10))); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized file: %v", err)
	}
	// A tiny file that declares enormous dimensions must be refused before decoding any pixels.
	var b bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, 5000, 5000)) // 25 MP of zeros compresses to a few KB
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if b.Len() > 1<<20 {
		t.Fatalf("test setup: bomb is %d bytes", b.Len())
	}
	if _, err := Process(&b); !errors.Is(err, ErrTooLarge) {
		t.Errorf("pixel bomb: %v, want ErrTooLarge", err)
	}
}

func TestOrientationRotatesPixels(t *testing.T) {
	// 4x2 image, red left half / blue right half; orientation 6 means "rotate 90° clockwise to display".
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			if x < 2 {
				src.Set(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				src.Set(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	got := orient(src, 6)
	if got.Bounds().Dx() != 2 || got.Bounds().Dy() != 4 {
		t.Fatalf("rotated bounds = %v, want 2x4", got.Bounds())
	}
	top := color.RGBAModel.Convert(got.At(0, 0)).(color.RGBA)
	bottom := color.RGBAModel.Convert(got.At(0, 3)).(color.RGBA)
	if top.R != 255 || bottom.B != 255 { // after a clockwise turn the left (red) side is on top
		t.Fatalf("pixels not rotated as expected: top=%v bottom=%v", top, bottom)
	}
}

func TestExifOrientationParsing(t *testing.T) {
	// Minimal JPEG: SOI, APP1 with a big-endian TIFF containing Orientation=6, then EOI.
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0, 0, 0, 0, 0}
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	size := len(app1) + 2
	jpg := append([]byte{0xFF, 0xD8, 0xFF, 0xE1, byte(size >> 8), byte(size)}, app1...)
	jpg = append(jpg, 0xFF, 0xD9)
	if got := exifOrientation(jpg); got != 6 {
		t.Fatalf("orientation = %d, want 6", got)
	}
	for _, junk := range [][]byte{nil, {0xFF}, {0xFF, 0xD8}, {0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF}} {
		if got := exifOrientation(junk); got != 1 {
			t.Errorf("malformed EXIF %v = %d, want 1", junk, got)
		}
	}
}

func TestStoreSaveServeDelete(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Process(bytes.NewReader(encodePNG(t, solid(40, 40, color.RGBA{5, 6, 7, 255}))))
	if err != nil {
		t.Fatal(err)
	}
	full, thumb, err := s.Save(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(full, "/media/") || !strings.Contains(thumb, "-t.") {
		t.Fatalf("urls %q %q", full, thumb)
	}
	if _, _, err := s.Save(p); err != nil { // idempotent
		t.Fatal(err)
	}

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + full)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" || !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("serve: %d %s %s", res.StatusCode, res.Header.Get("Content-Type"), res.Header.Get("Cache-Control"))
	}
	// Only generated names are reachable: no traversal, listings or dotfiles.
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("x"), 0o644)
	for _, bad := range []string{"/media/", "/media/secret.txt", "/media/../secret.txt", "/media/%2e%2e/secret.txt", "/media/" + p.Hash + ".gif"} {
		res, err := http.Get(srv.URL + bad)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Errorf("GET %s = %d, want 404", bad, res.StatusCode)
		}
	}

	if err := s.Delete(full, thumb, "/media/../secret.txt", "https://elsewhere/x.jpg"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.txt")); err != nil {
		t.Fatal("Delete must never touch files it did not create")
	}
	res, _ = http.Get(srv.URL + full)
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("deleted image still served: %d", res.StatusCode)
	}
}

func TestNewStoreRejectsUnwritableDir(t *testing.T) {
	if _, err := NewStore("/proc/definitely/not/writable"); err == nil {
		t.Fatal("expected an error for an unwritable directory")
	}
}
