package main

import (
	"fmt"
	"image"
	"strings"
)

// The sixel palette has 6 levels of red, 7 of green and 6 of blue: 252 colors.
const (
	redLevels   = 6
	greenLevels = 7
	blueLevels  = 6
)

// bayer is a 4 by 4 ordered dither matrix.
var bayer = [4][4]int{{0, 8, 2, 10}, {12, 4, 14, 6}, {3, 11, 1, 9}, {15, 7, 13, 5}}

// quantize maps a color value to one of the given levels. The dither matrix
// moves the threshold, so a smooth area does not show bands.
func quantize(v uint8, levels, x, y int) int {
	return (int(v)*(levels-1)*16 + bayer[y%4][x%4]*255 + 127) / (255 * 16)
}

// sixel encodes img as a sixel image of w by h pixels.
//
// ponytail: the palette is fixed and the dither is ordered, so a photo shows a
// pattern. The upgrade path is a palette that fits the image, such as median
// cut.
func sixel(img image.Image, w, h int) string {
	px := scale(img, w, h)
	index := make([]uint8, len(px))
	var used [redLevels * greenLevels * blueLevels]bool
	for i, p := range px {
		x, y := i%w, i/w
		c := (quantize(p[0], redLevels, x, y)*greenLevels+quantize(p[1], greenLevels, x, y))*blueLevels + quantize(p[2], blueLevels, x, y)
		index[i] = uint8(c)
		used[c] = true
	}

	var b strings.Builder
	// The second parameter keeps a pixel that the data does not set. The
	// raster attributes give a 1 to 1 pixel shape and the image size.
	fmt.Fprintf(&b, "\x1bP0;1;0q\"1;1;%d;%d", w, h)
	for c, ok := range used {
		if ok {
			// Sixel gives each color part as a percentage.
			fmt.Fprintf(&b, "#%d;2;%d;%d;%d", c,
				c/(greenLevels*blueLevels)*100/(redLevels-1),
				c/blueLevels%greenLevels*100/(greenLevels-1),
				c%blueLevels*100/(blueLevels-1))
		}
	}

	// A sixel character holds a column of 6 pixels of one color. Each band of
	// 6 rows has one pass for each color in it.
	for y := 0; y < h; y += 6 {
		var passes [len(used)][]byte
		for dy := 0; dy < 6 && y+dy < h; dy++ {
			for x := range w {
				c := index[(y+dy)*w+x]
				if passes[c] == nil {
					passes[c] = make([]byte, w)
				}
				passes[c][x] |= 1 << dy
			}
		}
		first := true
		for c, pass := range passes {
			if pass == nil {
				continue
			}
			if !first {
				b.WriteByte('$') // go back to the start of the band
			}
			first = false
			fmt.Fprintf(&b, "#%d", c)
			for x := 0; x < w; {
				n := 1
				for x+n < w && pass[x+n] == pass[x] {
					n++
				}
				if pass[x] == 0 && x+n == w {
					break // the pass sets no more pixels
				}
				if ch := '?' + pass[x]; n > 3 {
					fmt.Fprintf(&b, "!%d%c", n, ch)
				} else {
					b.WriteString(strings.Repeat(string(ch), n))
				}
				x += n
			}
		}
		b.WriteByte('-') // go to the next band
	}
	b.WriteString("\x1b\\")
	return b.String()
}
