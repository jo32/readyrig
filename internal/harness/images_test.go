package harness

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putPNG(t *testing.T, root, name string, w, h int, alpha uint8) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x ^ y), alpha})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), b.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func sentImage(t *testing.T, o Output) (image.Config, string) {
	t.Helper()
	if len(o.Images) != 1 {
		t.Fatalf("images: %d", len(o.Images))
	}
	raw, err := base64.StdEncoding.DecodeString(o.Images[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, format
}

func TestReadFileScalesLargeImages(t *testing.T) {
	r, _, root := filesForTest(t)
	putPNG(t, root, "sim.png", 1206, 2622, 255)
	out, err := invoke(t, r, "read_file", map[string]any{"path": "sim.png"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, format := sentImage(t, out)
	v := asMap(t, out)
	// 2622 -> 1568 on the long edge; 1206 * 1568/2622 = 721.2.
	if format != "jpeg" || cfg.Height != 1568 || cfg.Width != 721 || v["mime"] != "image/jpeg" {
		t.Fatalf("scaled to %s %dx%d, value %v", format, cfg.Width, cfg.Height, v)
	}
	if s := v["original_size"].([]int); s[0] != 1206 || s[1] != 2622 {
		t.Fatalf("original_size %v", s)
	}
	out, err = invoke(t, r, "read_file", map[string]any{"path": "sim.png", "max_size": 800})
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _ := sentImage(t, out); cfg.Height != 800 {
		t.Fatalf("max_size ignored: %dx%d", cfg.Width, cfg.Height)
	}
}

func TestReadFileSendsSmallImagesUnchanged(t *testing.T) {
	r, _, root := filesForTest(t)
	raw := putPNG(t, root, "icon.png", 120, 80, 255)
	out, err := invoke(t, r, "read_file", map[string]any{"path": "icon.png"})
	if err != nil {
		t.Fatal(err)
	}
	v := asMap(t, out)
	if out.Images[0].Data != base64.StdEncoding.EncodeToString(raw) || v["mime"] != "image/png" || v["original_size"] != nil {
		t.Fatalf("small image was changed: %v", v)
	}
	if s := v["image_size"].([]int); s[0] != 120 || s[1] != 80 {
		t.Fatalf("image_size %v", s)
	}
}

func TestReadFileRegionZoomsIntoTheOriginal(t *testing.T) {
	r, _, root := filesForTest(t)
	putPNG(t, root, "shot.png", 3000, 2000, 255)
	out, err := invoke(t, r, "read_file", map[string]any{"path": "shot.png", "region": []float64{100, 200, 700, 600}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _ := sentImage(t, out); cfg.Width != 600 || cfg.Height != 400 {
		t.Fatalf("region not at full resolution: %dx%d", cfg.Width, cfg.Height)
	}
	for _, region := range [][]float64{{0, 0, 3001, 10}, {10, 10, 5, 20}, {-1, 0, 5, 5}} {
		if _, err := invoke(t, r, "read_file", map[string]any{"path": "shot.png", "region": region}); err == nil {
			t.Fatalf("bad region %v accepted", region)
		}
	}
}

func TestReadFileKeepsTransparency(t *testing.T) {
	r, _, root := filesForTest(t)
	putPNG(t, root, "logo.png", 2000, 500, 128)
	out, err := invoke(t, r, "read_file", map[string]any{"path": "logo.png"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg, format := sentImage(t, out); format != "png" || cfg.Width != 1568 {
		t.Fatalf("transparent image sent as %s %dx%d", format, cfg.Width, cfg.Height)
	}
}

func TestReadFileImageOptionsAreChecked(t *testing.T) {
	r, _, root := filesForTest(t)
	put(t, root, "notes.txt", "hello\n")
	put(t, root, "broken.png", "\x89PNG fake")
	putPNG(t, root, "ok.png", 10, 10, 255)
	for _, args := range []map[string]any{
		{"path": "notes.txt", "max_size": 500},
		{"path": "ok.png", "max_size": 500, "encoding": "base64"},
		{"path": "ok.png", "max_size": 10},
		{"path": "broken.png", "region": []float64{0, 0, 1, 1}},
	} {
		if _, err := invoke(t, r, "read_file", args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

// Compact tool listings show only the first sentence, so it has to say that images work.
func TestReadFileFirstSentenceMentionsImages(t *testing.T) {
	r, _, _ := filesForTest(t)
	for _, tool := range r.tools {
		if tool.Spec.Name == "read_file" {
			if s := firstSentence(tool.Spec.Description); !strings.Contains(s, "image") {
				t.Fatalf("first sentence hides images: %q", s)
			}
			return
		}
	}
	t.Fatal("read_file not registered")
}
