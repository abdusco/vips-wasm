package govips

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/webp"
)

// testSource returns a 200x100 PNG with three vertical bands:
// red [0,50), green [50,150), blue [150,200).
func testSource(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			c := color.RGBA{R: 255, A: 255}
			switch {
			case x >= 150:
				c = color.RGBA{B: 255, A: 255}
			case x >= 50:
				c = color.RGBA{G: 255, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func decode(t *testing.T, format Format, data []byte) image.Image {
	t.Helper()
	var (
		img image.Image
		err error
	)
	switch format {
	case FormatWebP:
		img, err = webp.Decode(bytes.NewReader(data))
	default:
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	require.NoError(t, err)
	return img
}

func decodeConfig(t *testing.T, format Format, data []byte) image.Config {
	t.Helper()
	var (
		cfg image.Config
		err error
	)
	switch format {
	case FormatWebP:
		cfg, err = webp.DecodeConfig(bytes.NewReader(data))
	default:
		cfg, _, err = image.DecodeConfig(bytes.NewReader(data))
	}
	require.NoError(t, err)
	return cfg
}

func TestResizeDimensions(t *testing.T) {
	src := testSource(t)

	tests := []struct {
		name string
		opts ResizeOptions
		w, h int
	}{
		{"fit 100x50", ResizeOptions{Width: 100, Height: 50, Mode: ModeFit}, 100, 50},
		{"fit default mode", ResizeOptions{Width: 100, Height: 50}, 100, 50},
		{"fit fits inside box", ResizeOptions{Width: 100, Height: 100}, 100, 50},
		{"fit width only", ResizeOptions{Width: 100}, 100, 50},
		{"fit does not upscale", ResizeOptions{Width: 400, Height: 200, Mode: ModeFit}, 200, 100},
		{"crop 50x50", ResizeOptions{Width: 50, Height: 50, Mode: ModeCrop}, 50, 50},
		{"crop 30x60", ResizeOptions{Width: 30, Height: 60, Mode: ModeCrop}, 30, 60},
		{"crop upscale 400x200", ResizeOptions{Width: 400, Height: 200, Mode: ModeCrop}, 400, 200},
		{"crop upscale 300x300", ResizeOptions{Width: 300, Height: 300, Mode: ModeCrop}, 300, 300},
		{"force 50x50", ResizeOptions{Width: 50, Height: 50, Mode: ModeForce}, 50, 50},
		{"force upscale 400x300", ResizeOptions{Width: 400, Height: 300, Mode: ModeForce}, 400, 300},
	}

	for _, format := range []Format{FormatPNG, FormatWebP} {
		for _, tt := range tests {
			t.Run(string(format)+"/"+tt.name, func(t *testing.T) {
				opts := tt.opts
				opts.Format = format

				out, err := Resize(context.Background(), src, opts)
				require.NoError(t, err)

				cfg := decodeConfig(t, format, out)
				assert.Equal(t, tt.w, cfg.Width, "width")
				assert.Equal(t, tt.h, cfg.Height, "height")
			})
		}
	}
}

func TestResizeInvalidOptions(t *testing.T) {
	src := testSource(t)

	tests := []struct {
		name string
		opts ResizeOptions
	}{
		{"crop without height", ResizeOptions{Width: 50, Mode: ModeCrop}},
		{"force without height", ResizeOptions{Width: 50, Mode: ModeForce}},
		{"unknown mode", ResizeOptions{Width: 50, Height: 50, Mode: "zoom"}},
	}

	for _, format := range []Format{FormatPNG, FormatWebP} {
		for _, tt := range tests {
			t.Run(string(format)+"/"+tt.name, func(t *testing.T) {
				opts := tt.opts
				opts.Format = format

				_, err := Resize(context.Background(), src, opts)
				assert.ErrorIs(t, err, ErrInvalidOptions)
			})
		}
	}
}

// A centre crop of the middle of the source must be all green. A crop
// anchored left or right would pick up the red or blue bands.
func TestResizeCropIsCentred(t *testing.T) {
	src := testSource(t)

	for _, format := range []Format{FormatPNG, FormatWebP} {
		t.Run(string(format), func(t *testing.T) {
			out, err := Resize(context.Background(), src, ResizeOptions{
				Width: 50, Height: 50, Format: format, Mode: ModeCrop,
			})
			require.NoError(t, err)

			img := decode(t, format, out)
			// Corners and centre; lossy WebP needs a tolerance.
			for _, p := range []image.Point{{2, 2}, {47, 2}, {2, 47}, {47, 47}, {25, 25}} {
				r, g, b, _ := img.At(p.X, p.Y).RGBA()
				assert.Greater(t, g>>8, uint32(200), "green at %v", p)
				assert.Less(t, r>>8, uint32(50), "red at %v", p)
				assert.Less(t, b>>8, uint32(50), "blue at %v", p)
			}
		})
	}
}

// Force stretches the whole source, so all three bands stay visible.
func TestResizeForceKeepsWholeImage(t *testing.T) {
	out, err := Resize(context.Background(), testSource(t), ResizeOptions{
		Width: 50, Height: 50, Format: FormatPNG, Mode: ModeForce,
	})
	require.NoError(t, err)

	img := decode(t, FormatPNG, out)
	r, _, _, _ := img.At(2, 25).RGBA()
	_, _, b, _ := img.At(47, 25).RGBA()
	assert.Greater(t, r>>8, uint32(200), "left edge should be red")
	assert.Greater(t, b>>8, uint32(200), "right edge should be blue")
}
