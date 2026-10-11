package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"sync"
)

// These limits bound the work that one image in a message can cause.
const (
	maxImageBytes  = 10 << 20
	maxImagePixels = 40 << 20
)

// decodeImage decodes a PNG, JPEG or GIF image. It reads the size first, so
// that a small file cannot make it allocate a very large image.
func decodeImage(b []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > maxImagePixels {
		return nil, errors.New("image is too large")
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, err
}

// localImage returns the bytes of an image that the message carries: a cid:
// reference to an inline part, or a base64 data: URI. It returns nil for any
// other source.
func localImage(m message, src string) []byte {
	if cid, ok := strings.CutPrefix(src, "cid:"); ok {
		return m.inline(cid)
	}
	if meta, data, ok := strings.Cut(src, ","); ok && strings.HasPrefix(meta, "data:") && strings.HasSuffix(meta, ";base64") {
		b, _ := base64.StdEncoding.DecodeString(data)
		return b
	}
	return nil
}

// fetchImages downloads and decodes the given image URLs. It leaves out an
// image that fails.
func fetchImages(ctx context.Context, srcs []string) map[string]image.Image {
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	out := map[string]image.Image{}
	for _, src := range srcs {
		wg.Go(func() {
			img, err := fetchImage(ctx, src)
			if err != nil {
				return
			}
			mu.Lock()
			out[src] = img
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

func fetchImage(ctx context.Context, src string) (image.Image, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return nil, err
	}
	return decodeImage(b)
}

// halfBlocks draws img in the given number of columns. Each cell is an upper
// half block. The foreground color is the top pixel and the background color is
// the bottom pixel. The output needs a terminal with 24-bit color.
//
// A half block gives 2 pixels for each cell, so text in an image is not
// readable. The sixel view shows one image at the full resolution.
func halfBlocks(img image.Image, cols int) []string {
	b := img.Bounds()
	rows := max(cols*b.Dy()/b.Dx()/2, 1)
	px := scale(img, cols, rows*2)

	lines := make([]string, rows)
	for y := range rows {
		var sb strings.Builder
		for x := range cols {
			top, bottom := px[2*y*cols+x], px[(2*y+1)*cols+x]
			fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm▀", top[0], top[1], top[2], bottom[0], bottom[1], bottom[2])
		}
		sb.WriteString("\x1b[0m")
		lines[y] = sb.String()
	}
	return lines
}

// scale resizes img to w by h pixels with a box filter, and returns the RGB
// pixels in row order. It puts a transparent pixel on white, because mail is
// designed for a white background. It reads about 4 by 4 samples for each
// pixel, which keeps a large image fast.
func scale(img image.Image, w, h int) [][3]uint8 {
	b := img.Bounds()
	out := make([][3]uint8, w*h)
	for y := range h {
		y0, y1 := b.Min.Y+y*b.Dy()/h, b.Min.Y+(y+1)*b.Dy()/h
		for x := range w {
			x0, x1 := b.Min.X+x*b.Dx()/w, b.Min.X+(x+1)*b.Dx()/w
			var sum [3]uint32
			var n uint32
			for sy := y0; sy < max(y1, y0+1); sy += max((y1-y0)/4, 1) {
				for sx := x0; sx < max(x1, x0+1); sx += max((x1-x0)/4, 1) {
					// RGBA returns 16-bit values that the alpha already multiplies.
					r, g, bl, a := img.At(sx, sy).RGBA()
					sum[0] += r + 0xffff - a
					sum[1] += g + 0xffff - a
					sum[2] += bl + 0xffff - a
					n++
				}
			}
			out[y*w+x] = [3]uint8{uint8(sum[0] / n >> 8), uint8(sum[1] / n >> 8), uint8(sum[2] / n >> 8)}
		}
	}
	return out
}
