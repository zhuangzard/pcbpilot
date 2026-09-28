package designreport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
)

// DefaultMaxImageBytes is the size above which raster images are halved
// until they fit (the report embeds them as base64).
const DefaultMaxImageBytes = 1 << 20

// PrepareImage sniffs, measures and (if larger than maxBytes) downscales an
// image. SHA256 is of the original file (provenance); Asset is named by the
// hash of the stored bytes so identical pictures are shared across versions.
func PrepareImage(kind, label, path string, data []byte, maxBytes int) (Image, error) {
	img := Image{Kind: kind, Label: label, Path: path}
	h := sha256.Sum256(data)
	img.SHA256 = hex.EncodeToString(h[:])
	mime := http.DetectContentType(data)
	ext := ""
	switch {
	case strings.HasPrefix(mime, "image/png"):
		mime, ext = "image/png", "png"
	case strings.HasPrefix(mime, "image/jpeg"):
		mime, ext = "image/jpeg", "jpg"
	case bytes.Contains(data[:min(len(data), 512)], []byte("<svg")) || strings.HasSuffix(strings.ToLower(path), ".svg"):
		mime, ext = "image/svg+xml", "svg"
	default:
		return img, fmt.Errorf("%s: unsupported image type %s (png, jpeg, svg)", path, mime)
	}
	img.Mime = mime
	if ext != "svg" {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return img, fmt.Errorf("%s: %w", path, err)
		}
		img.Width, img.Height = cfg.Width, cfg.Height
		if maxBytes > 0 && len(data) > maxBytes {
			src, _, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				return img, fmt.Errorf("%s: %w", path, err)
			}
			for len(data) > maxBytes && src.Bounds().Dx() > 64 {
				src = halve(src)
				var buf bytes.Buffer
				if ext == "png" {
					enc := png.Encoder{CompressionLevel: png.BestCompression}
					err = enc.Encode(&buf, src)
				} else {
					err = jpeg.Encode(&buf, src, &jpeg.Options{Quality: 85})
				}
				if err != nil {
					return img, err
				}
				data = buf.Bytes()
				img.Resized = true
			}
			img.Width, img.Height = src.Bounds().Dx(), src.Bounds().Dy()
		}
	}
	img.Data = data
	img.Bytes = len(data)
	hs := sha256.Sum256(data)
	img.Asset = hex.EncodeToString(hs[:])[:16] + "." + ext
	return img, nil
}

// halve is a 2×2 box-filter downscale.
func halve(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx()/2, b.Dy()/2
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl, a uint32
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					c := color.NRGBAModel.Convert(src.At(b.Min.X+2*x+dx, b.Min.Y+2*y+dy)).(color.NRGBA)
					r, g, bl, a = r+uint32(c.R), g+uint32(c.G), bl+uint32(c.B), a+uint32(c.A)
				}
			}
			dst.SetNRGBA(x, y, color.NRGBA{uint8(r / 4), uint8(g / 4), uint8(bl / 4), uint8(a / 4)})
		}
	}
	return dst
}
