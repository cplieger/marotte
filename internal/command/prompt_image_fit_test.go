package command

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFitImage_InCapImagePassesThroughByteIdentical(t *testing.T) {
	data := pngBytes(t, 120, 80)

	out, mime, reason := fitImage(data, MaxImageEdgePx, MaxInlineEncodedBytes)

	if reason != "" {
		t.Fatalf("fitImage(120x80 png) reason = %q, want none", reason)
	}
	if !bytes.Equal(out, data) {
		t.Errorf("fitImage(120x80 png) returned %d different bytes, want the input unchanged", len(out))
	}
	if mime != "image/png" {
		t.Errorf("fitImage(120x80 png) mime = %q, want image/png", mime)
	}
}

func TestFitImage_ShrinksUntilTheEncodedCapFits(t *testing.T) {
	data := noisePNG(t, 600, 600)
	const maxEncoded = 700 << 10
	if base64.StdEncoding.EncodedLen(len(data)) <= maxEncoded {
		t.Fatalf("Setup: fixture encodes to %d, want it over %d", base64.StdEncoding.EncodedLen(len(data)), maxEncoded)
	}

	out, _, reason := fitImage(data, MaxImageEdgePx, maxEncoded)

	if reason != "" {
		t.Fatalf("fitImage(600x600 noise, cap %d) reason = %q, want a fit", maxEncoded, reason)
	}
	if got := base64.StdEncoding.EncodedLen(len(out)); got > maxEncoded {
		t.Errorf("fitImage(600x600 noise) encodes to %d, want <= %d", got, maxEncoded)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("fitted bytes are not a PNG: %v", err)
	}
	if long := max(cfg.Width, cfg.Height); long >= 600 || long < MinImageEdgePx {
		t.Errorf("fitImage(600x600 noise) long edge = %d, want under 600 and at least %d", long, MinImageEdgePx)
	}
}

// The header alone decides the ceiling: this file declares 30000x30000 over a
// body that is not there, so a full decode would only report it undecodable.
func TestFitImage_RefusesPastThePixelCeilingWithoutDecoding(t *testing.T) {
	data := pngHeaderOnly(t, 30000, 30000)

	_, _, reason := fitImage(data, MaxImageEdgePx, MaxInlineEncodedBytes)

	if !strings.Contains(reason, "megapixels") {
		t.Errorf("fitImage(30000x30000 header) reason = %q, want the pixel-ceiling reason", reason)
	}
}

func TestFitImage_OverCapWebPReencodesAsPNG(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "blue-purple-pink-large.lossy.webp"))
	if err != nil {
		t.Fatal(err)
	}

	out, mime, reason := fitImage(data, 300, MaxInlineEncodedBytes)

	if reason != "" {
		t.Fatalf("fitImage(600x400 webp, edge 300) reason = %q, want a fit", reason)
	}
	if mime != "image/png" {
		t.Errorf("fitImage(webp) mime = %q, want image/png: the bytes are re-encoded as PNG", mime)
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(out)); err != nil || format != "png" {
		t.Errorf("fitImage(webp) output decodes as %q (%v), want png", format, err)
	}
}

func TestFitImage_UndecodableBytesAreRefused(t *testing.T) {
	if _, _, reason := fitImage([]byte("not an image"), MaxImageEdgePx, MaxInlineEncodedBytes); reason == "" {
		t.Error("fitImage(garbage) reason empty, want a refusal")
	}
}

// pngHeaderOnly builds the PNG signature plus an IHDR declaring w x h RGBA, and
// nothing after it.
func pngHeaderOnly(t *testing.T, w, h int) []byte {
	t.Helper()
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(h))
	ihdr[8], ihdr[9] = 8, 6
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(ihdr)))
	chunk := append([]byte("IHDR"), ihdr[:]...)
	buf.Write(chunk)
	_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return buf.Bytes()
}
