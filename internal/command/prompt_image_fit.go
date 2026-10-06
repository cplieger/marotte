package command

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"strconv"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// MaxImageEdgePx is the long edge an inlined image is fitted to. The backend
// refuses a request carrying more than about 20 images when any one is over
// 2000px, and tool-returned images persist into the replayed history uncounted
// by MaxHistoryInlineImages, so that rule can bind on any chat.
const MaxImageEdgePx = 2000

// MinImageEdgePx is the smallest long edge the shrink loop will produce before
// it gives up and the image goes as a path reference.
const MinImageEdgePx = 256

// MaxImageSourcePixels bounds what is fully decoded: past it the image goes as a
// path reference, read off the header alone, so a crafted file cannot claim
// gigabytes of decode memory.
const MaxImageSourcePixels = 64_000_000

// jpegQuality is the re-encode quality for a JPEG source.
const jpegQuality = 85

// shrinkStep is the factor the long edge drops by each time an encoding is still
// over the encoded cap.
const shrinkStep = 0.75

// imageFormatMIME names the MIME type for each format image.Decode can answer
// here; a format outside it is one the model is not sent.
var imageFormatMIME = map[string]string{
	"png":  mimePNG,
	"jpeg": mimeJPEG,
	"gif":  mimeGIF,
	"webp": mimeWebP,
}

// The image MIME types an attachment can carry.
const (
	mimePNG  = "image/png"
	mimeJPEG = "image/jpeg"
	mimeGIF  = "image/gif"
	mimeWebP = "image/webp"
)

// fitImage returns data as an image the model can take: at most maxEdge on its
// long edge and at most maxEncoded once base64'd, with the MIME type of the bytes
// it returns. An image already inside both limits comes back byte-identical. A
// non-empty reason means no fit was possible and the caller sends a path.
func fitImage(data []byte, maxEdge, maxEncoded int) (out []byte, outMime, reason string) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", "its image data could not be decoded"
	}
	mime, ok := imageFormatMIME[format]
	if !ok {
		return nil, "", "its format (" + format + ") is not one the model accepts"
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxImageSourcePixels {
		return nil, "", "it is larger than " + strconv.Itoa(MaxImageSourcePixels/1_000_000) + " megapixels"
	}
	long := max(cfg.Width, cfg.Height)
	if long <= maxEdge && base64.StdEncoding.EncodedLen(len(data)) <= maxEncoded {
		return data, mime, ""
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", "its image data could not be decoded"
	}
	return shrinkToFit(img, format == "jpeg", min(long, maxEdge), maxEncoded)
}

// shrinkToFit encodes img with its long edge at edge, stepping the edge down by
// shrinkStep until the encoding fits maxEncoded or would drop under
// MinImageEdgePx. A JPEG source stays JPEG; everything else becomes PNG, since
// the standard library has no WebP encoder and keeps only a GIF's first frame.
func shrinkToFit(img image.Image, asJPEG bool, edge, maxEncoded int) (out []byte, outMime, reason string) {
	for edge >= MinImageEdgePx {
		scaled := scaleToEdge(img, edge, !asJPEG)
		var buf bytes.Buffer
		var err error
		if asJPEG {
			err = jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: jpegQuality})
		} else {
			err = png.Encode(&buf, scaled)
		}
		if err != nil {
			return nil, "", "it could not be re-encoded"
		}
		if base64.StdEncoding.EncodedLen(buf.Len()) <= maxEncoded {
			if asJPEG {
				return buf.Bytes(), mimeJPEG, ""
			}
			return buf.Bytes(), mimePNG, ""
		}
		edge = int(float64(edge) * shrinkStep)
	}
	return nil, "", "it stays over the inline size limit even at " + strconv.Itoa(MinImageEdgePx) + "px"
}

// scaleToEdge resamples img so its long edge is edge, keeping the aspect ratio.
// keepAlpha selects a non-premultiplied target so a PNG keeps its transparency.
func scaleToEdge(img image.Image, edge int, keepAlpha bool) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w >= h {
		h = max(1, (h*edge+w/2)/w)
		w = edge
	} else {
		w = max(1, (w*edge+h/2)/h)
		h = edge
	}
	rect := image.Rect(0, 0, w, h)
	var dst draw.Image
	if keepAlpha {
		dst = image.NewNRGBA(rect)
	} else {
		dst = image.NewRGBA(rect)
	}
	draw.CatmullRom.Scale(dst, rect, img, b, draw.Src, nil)
	return dst
}
