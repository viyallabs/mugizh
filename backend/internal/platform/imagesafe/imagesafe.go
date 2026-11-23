// Package imagesafe re-encodes uploaded images so that nothing but pixels is stored (TM-42, E-DP-20, TC-14): no
// EXIF or other metadata (a phone photo carries the GPS location of where it was taken, the device and the time), no
// comments or text chunks, no bytes appended after the image (polyglot files). Dimensions are checked before
// decoding, so a small file that claims a huge canvas (a decompression bomb) is refused. The EXIF orientation of a
// JPEG is applied to the pixels first, so a photographed document stays upright after the metadata is gone.
package imagesafe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
)

// Limits on what is decoded.
const (
	MaxSide     = 12000      // pixels on either side
	MaxPixels   = 40_000_000 // about 160 MB as RGBA once decoded
	jpegQuality = 92
)

// ErrInvalid is returned for data that is not a decodable image of the stated type, or is too large to decode.
var ErrInvalid = errors.New("imagesafe: not an acceptable image")

// Reencode returns the image re-encoded in the same format. contentType is "image/jpeg" or "image/png".
func Reencode(data []byte, contentType string) ([]byte, error) {
	var format string
	switch contentType {
	case "image/jpeg":
		format = "jpeg"
	case "image/png":
		format = "png"
	default:
		return nil, ErrInvalid
	}
	cfg, got, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || got != format {
		return nil, ErrInvalid
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxSide || cfg.Height > MaxSide || cfg.Width*cfg.Height > MaxPixels {
		return nil, ErrInvalid
	}
	var img image.Image
	if format == "jpeg" {
		img, err = jpeg.Decode(bytes.NewReader(data))
	} else {
		img, err = png.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, ErrInvalid
	}
	var out bytes.Buffer
	if format == "jpeg" {
		img = orient(img, jpegOrientation(data))
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: jpegQuality})
	} else {
		err = (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&out, img)
	}
	if err != nil {
		return nil, ErrInvalid
	}
	return out.Bytes(), nil
}

// jpegOrientation reads the EXIF orientation tag (1 to 8) from a JPEG's APP1 segment; 1 when absent or unreadable.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan or end: no more metadata
			return 1
		}
		size := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if size < 2 || i+2+size > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off : off+2]))
	for k := 0; k < n; k++ {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:e+2]) == 0x0112 { // Orientation, SHORT
			v := int(bo.Uint16(t[e+8 : e+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient applies an EXIF orientation to the pixels.
func orient(src image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	ow, oh := w, h
	if o >= 5 {
		ow, oh = h, w
	}
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // rotate 90 clockwise
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 90 counter-clockwise
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, rgba.At(x, y))
		}
	}
	return dst
}
