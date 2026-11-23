package imagesafe_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/imagesafe"
)

func picture(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 7), uint8(y * 5), 120, 255})
		}
	}
	return img
}

// withExif inserts an APP1 Exif segment after SOI: an orientation tag and a marker string standing in for GPS data.
func withExif(j []byte, orientation uint16, marker string) []byte {
	var t bytes.Buffer
	t.WriteString("MM\x00\x2a")
	_ = binary.Write(&t, binary.BigEndian, uint32(8))
	_ = binary.Write(&t, binary.BigEndian, uint16(1))
	_ = binary.Write(&t, binary.BigEndian, []uint16{0x0112, 3})
	_ = binary.Write(&t, binary.BigEndian, uint32(1))
	_ = binary.Write(&t, binary.BigEndian, []uint16{orientation, 0})
	_ = binary.Write(&t, binary.BigEndian, uint32(0))
	t.WriteString(marker)
	seg := append([]byte("Exif\x00\x00"), t.Bytes()...)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(seg)+2))
	out = append(out, seg...)
	return append(out, j[2:]...)
}

func TestReencodeStripsMetadataAndTrailingBytes(t *testing.T) {
	var j bytes.Buffer
	if err := jpeg.Encode(&j, picture(40, 20), nil); err != nil {
		t.Fatal(err)
	}
	in := append(withExif(j.Bytes(), 1, "GPS-12.9716N-77.5946E"), []byte("PK\x03\x04<script>appended payload</script>")...)
	out, err := imagesafe.Reencode(in, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"GPS-12.9716N", "Exif", "<script>", "PK\x03\x04"} {
		if bytes.Contains(out, []byte(leak)) {
			t.Fatalf("%q survived re-encoding", leak)
		}
	}
	if cfg, f, err := image.DecodeConfig(bytes.NewReader(out)); err != nil || f != "jpeg" || cfg.Width != 40 || cfg.Height != 20 {
		t.Fatalf("output: %v %s %+v", err, f, cfg)
	}
}

func TestReencodeAppliesOrientation(t *testing.T) {
	var j bytes.Buffer
	_ = jpeg.Encode(&j, picture(40, 20), nil)
	out, err := imagesafe.Reencode(withExif(j.Bytes(), 6, ""), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _, _ := image.DecodeConfig(bytes.NewReader(out)); cfg.Width != 20 || cfg.Height != 40 {
		t.Fatalf("a 90-degree photo must be turned upright: %+v", cfg)
	}
}

func TestReencodePNGDropsTextChunks(t *testing.T) {
	var p bytes.Buffer
	_ = png.Encode(&p, picture(10, 10))
	b := p.Bytes()
	// Insert a tEXt chunk (with a valid CRC) after IHDR (8-byte signature + 25-byte IHDR chunk).
	text := []byte("tEXtComment\x00home address 12 Synthetic Road")
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(text)-4))
	chunk = append(chunk, text...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(text))
	in := append(append(append([]byte{}, b[:33]...), chunk...), b[33:]...)
	out, err := imagesafe.Reencode(in, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("home address")) {
		t.Fatal("PNG text chunk survived")
	}
}

func TestReencodeRefusesBombsWrongTypesAndGarbage(t *testing.T) {
	var j bytes.Buffer
	_ = jpeg.Encode(&j, picture(8, 8), nil)
	// Claim a 60000 x 60000 canvas in the SOF0 header.
	bomb := append([]byte{}, j.Bytes()...)
	for i := 2; i+9 < len(bomb); i++ {
		if bomb[i] == 0xFF && bomb[i+1] == 0xC0 {
			binary.BigEndian.PutUint16(bomb[i+5:], 60000)
			binary.BigEndian.PutUint16(bomb[i+7:], 60000)
			break
		}
	}
	for name, c := range map[string]struct {
		data []byte
		ct   string
	}{
		"bomb":        {bomb, "image/jpeg"},
		"jpeg as png": {j.Bytes(), "image/png"},
		"garbage":     {[]byte("\xFF\xD8\xFFnot really a jpeg"), "image/jpeg"},
		"pdf":         {[]byte("%PDF-1.4"), "application/pdf"},
		"truncated":   {j.Bytes()[:20], "image/jpeg"},
	} {
		if _, err := imagesafe.Reencode(c.data, c.ct); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}
