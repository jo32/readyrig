package harness

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"math"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// defaultImageEdge is the longest edge the model looks at; the model API scales
	// anything larger down anyway, so sending more only costs transfer time.
	defaultImageEdge = 1568
	minImageEdge     = 64
	maxImageEdge     = 4096
	// keepImageBytes: an image that already fits max_size and is this small is sent as is.
	keepImageBytes = 1 << 20
	// maxImageInput bounds the file read_file decodes, maxImagePixels its decoded size
	// (about 200 MB of RGBA), and maxImageBytes what is returned.
	maxImageInput  = 64 << 20
	maxImagePixels = 50_000_000
	maxImageBytes  = 5 << 20
)

// imageRequest holds read_file's image options: MaxSize is the longest edge in pixels
// (0 = default) and Region an [x1,y1,x2,y2] area of the original in pixels.
type imageRequest struct {
	MaxSize int
	Region  []float64
}

type preparedImage struct {
	MIME          string
	Data          []byte
	Width, Height int // what is sent; 0 when the image could not be decoded
	OrigW, OrigH  int
	Changed       bool // cropped, scaled or re-encoded
}

// prepareImage returns the picture to send. One that already fits is sent unchanged;
// otherwise it is cropped to Region, scaled so its longest edge is at most MaxSize and
// re-encoded: JPEG when it is opaque, PNG when it has transparency.
func prepareImage(raw []byte, mime string, req imageRequest) (preparedImage, error) {
	edge := req.MaxSize
	if edge == 0 {
		edge = defaultImageEdge
	}
	if edge < minImageEdge || edge > maxImageEdge {
		return preparedImage{}, fmt.Errorf("max_size must be %d to %d", minImageEdge, maxImageEdge)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		// A variant Go cannot decode is sent as it is, as long as it is small enough.
		if len(req.Region) > 0 || len(raw) > maxImageBytes {
			return preparedImage{}, fmt.Errorf("cannot decode the image to resize it: %w", err)
		}
		return preparedImage{MIME: mime, Data: raw}, nil
	}
	out := preparedImage{OrigW: cfg.Width, OrigH: cfg.Height}
	if len(req.Region) == 0 && max(cfg.Width, cfg.Height) <= edge && len(raw) <= keepImageBytes {
		out.MIME, out.Data, out.Width, out.Height = mime, raw, cfg.Width, cfg.Height
		return out, nil
	}
	if cfg.Width*cfg.Height > maxImagePixels {
		return preparedImage{}, fmt.Errorf("image is %dx%d pixels, more than the %d megapixels read_file decodes", cfg.Width, cfg.Height, maxImagePixels/1_000_000)
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return preparedImage{}, fmt.Errorf("cannot decode the image to resize it: %w", err)
	}
	b := src.Bounds()
	crop := b
	if r := req.Region; len(r) > 0 {
		if len(r) != 4 || r[0] < 0 || r[1] < 0 || r[2] <= r[0] || r[3] <= r[1] || r[2] > float64(b.Dx()) || r[3] > float64(b.Dy()) {
			return preparedImage{}, fmt.Errorf("region must be [x1,y1,x2,y2] inside the %dx%d image", b.Dx(), b.Dy())
		}
		crop = image.Rect(int(math.Floor(r[0])), int(math.Floor(r[1])), int(math.Ceil(r[2])), int(math.Ceil(r[3]))).Add(b.Min).Intersect(b)
	}
	w, h := crop.Dx(), crop.Dy()
	if max(w, h) > edge {
		scale := float64(edge) / float64(max(w, h))
		w, h = max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == crop.Dx() && h == crop.Dy() {
		draw.Copy(dst, image.Point{}, src, crop, draw.Src, nil)
	} else {
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Src, nil)
	}
	var buf bytes.Buffer
	if dst.Opaque() {
		out.MIME = "image/jpeg"
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85})
	} else {
		out.MIME = "image/png"
		err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, dst)
	}
	if err != nil {
		return preparedImage{}, err
	}
	// Re-encoding alone (no crop, no scaling) is only worth it when it is smaller.
	if len(req.Region) == 0 && w == cfg.Width && h == cfg.Height && buf.Len() >= len(raw) && len(raw) <= maxImageBytes {
		out.MIME, out.Data, out.Width, out.Height = mime, raw, cfg.Width, cfg.Height
		return out, nil
	}
	if buf.Len() > maxImageBytes {
		return preparedImage{}, errors.New("image is still larger than 5 MiB after scaling; pass a smaller max_size or a region")
	}
	out.Data, out.Width, out.Height, out.Changed = buf.Bytes(), w, h, true
	return out, nil
}
