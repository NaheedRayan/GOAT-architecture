// Package media validates, normalises and stores uploaded images.
//
// Uploads are never stored as received: they are decoded (so anything that is
// not really a JPEG/PNG/GIF/WebP is refused), rotated according to their EXIF
// orientation, scaled down, and re-encoded. Re-encoding drops metadata (GPS
// coordinates etc.) and any payload hidden in the original file. Two sizes are
// produced: a card thumbnail and a full-size image.
package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // registers the GIF decoder
	"image/jpeg"
	"image/png"
	"io"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder
)

const (
	MaxUploadBytes = 8 << 20 // size of the file a user may upload
	MaxPixels      = 24_000_000
	FullMaxSide    = 1600
	ThumbMaxSide   = 600
	jpegQuality    = 85
)

// ErrInvalid is returned for files that are not acceptable images; its message is safe to show to users.
var ErrInvalid = errors.New("that file is not a valid image (use JPEG, PNG, GIF or WebP)")

// ErrTooLarge is returned for oversized files or images.
var ErrTooLarge = errors.New("that image is too large (max 8 MB and 24 megapixels)")

// Processed is an image ready to store.
type Processed struct {
	Hash  string // content hash of the uploaded bytes; stable names make uploads idempotent
	Ext   string // "jpg" or "png"
	Full  []byte
	Thumb []byte
}

// Process validates and normalises an upload.
func Process(r io.Reader) (Processed, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if err != nil {
		return Processed{}, err
	}
	if len(raw) > MaxUploadBytes {
		return Processed{}, ErrTooLarge
	}
	// Reject decompression bombs and non-images before decoding any pixels.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "jpeg" && format != "png" && format != "gif" && format != "webp") {
		return Processed{}, ErrInvalid
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > MaxPixels {
		return Processed{}, ErrTooLarge
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Processed{}, ErrInvalid
	}
	if format == "jpeg" {
		img = orient(img, exifOrientation(raw))
	}

	ext := "jpg"
	if hasAlpha(img) {
		ext = "png"
	}
	full, err := encode(fit(img, FullMaxSide), ext)
	if err != nil {
		return Processed{}, err
	}
	thumb, err := encode(fit(img, ThumbMaxSide), ext)
	if err != nil {
		return Processed{}, err
	}
	sum := sha256.Sum256(raw)
	return Processed{Hash: hex.EncodeToString(sum[:])[:20], Ext: ext, Full: full, Thumb: thumb}, nil
}

// fit scales img down so its longest side is at most max (never up).
func fit(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return img
	}
	if w >= h {
		h = h * max / w
		w = max
	} else {
		w = w * max / h
		h = max
	}
	w, h = maxInt(w, 1), maxInt(h, 1)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	return dst
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func encode(img image.Image, ext string) ([]byte, error) {
	var buf bytes.Buffer
	switch ext {
	case "png":
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
	default:
		// JPEG has no transparency: composite onto white.
		flat := image.NewRGBA(img.Bounds())
		draw.Draw(flat, flat.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
		draw.Draw(flat, flat.Bounds(), img, img.Bounds().Min, draw.Over)
		if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// hasAlpha reports whether any pixel is not fully opaque.
func hasAlpha(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return true
			}
		}
	}
	return false
}

// exifOrientation reads the orientation tag (1-8) from a JPEG's EXIF block, or 1 if absent.
func exifOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan / end: no EXIF after this
			return 1
		}
		size := int(b[i+2])<<8 | int(b[i+3])
		if size < 2 || i+2+size > len(b) {
			return 1
		}
		if marker == 0xE1 && size >= 16 && string(b[i+4:i+10]) == "Exif\x00\x00" {
			return parseTIFFOrientation(b[i+10 : i+2+size])
		}
		i += 2 + size
	}
	return 1
}

func parseTIFFOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var u16 func([]byte) int
	var u32 func([]byte) int
	switch string(t[:2]) {
	case "II":
		u16 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 }
		u32 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24 }
	case "MM":
		u16 = func(b []byte) int { return int(b[0])<<8 | int(b[1]) }
		u32 = func(b []byte) int { return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3]) }
	default:
		return 1
	}
	ifd := u32(t[4:8])
	if ifd < 8 || ifd+2 > len(t) {
		return 1
	}
	n := u16(t[ifd:])
	for k := 0; k < n; k++ {
		e := ifd + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if u16(t[e:]) == 0x0112 { // Orientation
			if o := u16(t[e+8:]); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

// orient applies an EXIF orientation so the pixels are upright.
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := o >= 5
	nw, nh := w, h
	if swap {
		nw, nh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
