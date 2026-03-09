package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/abdusco/vips-wasm/govips"
)

func extToFormat(ext string) (govips.Format, error) {
	switch ext {
	case ".jpg", ".jpeg":
		return govips.FormatJPEG, nil
	case ".png":
		return govips.FormatPNG, nil
	case ".webp":
		return govips.FormatWebP, nil
	case ".avif":
		return govips.FormatAVIF, nil
	default:
		return "", fmt.Errorf("unsupported output extension %q", ext)
	}
}

func main() {
	quality := flag.Int("q", 0, "output quality 1-100 (0 = encoder default)")
	keepMeta := flag.Bool("keep-metadata", false, "keep image metadata (stripped by default)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: vips-thumb [flags] <input> <output> <width> [height]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  input   - source image (JPEG, PNG, WebP, ...)")
		fmt.Fprintln(os.Stderr, "  output  - destination path (format from extension)")
		fmt.Fprintln(os.Stderr, "  width   - target width in pixels")
		fmt.Fprintln(os.Stderr, "  height  - optional max height (0 = preserve aspect ratio)")
		fmt.Fprintln(os.Stderr, "")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) < 3 {
		flag.Usage()
		os.Exit(1)
	}

	inputPath := args[0]
	outputPath := args[1]

	width, err := strconv.Atoi(args[2])
	if err != nil || width <= 0 {
		fmt.Fprintf(os.Stderr, "error: width must be a positive integer, got %q\n", args[2])
		os.Exit(1)
	}

	height := 0
	if len(args) >= 4 {
		h, hErr := strconv.Atoi(args[3])
		if hErr != nil || h < 0 {
			fmt.Fprintf(os.Stderr, "error: height must be a non-negative integer, got %q\n", args[3])
			os.Exit(1)
		}
		height = h
	}

	if *quality < 0 || *quality > 100 {
		fmt.Fprintf(os.Stderr, "error: quality must be between 0 and 100, got %d\n", *quality)
		os.Exit(1)
	}

	format, err := extToFormat(strings.ToLower(filepath.Ext(outputPath)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	inputData, err := os.ReadFile(inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading input: %v\n", err)
		os.Exit(1)
	}

	out, err := govips.Resize(context.Background(), inputData, govips.ResizeOptions{
		Width:        width,
		Height:       height,
		Format:       format,
		Quality:      *quality,
		KeepMetadata: *keepMeta,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(outputPath, out, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing output: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("wrote %s (%d bytes)\n", outputPath, len(out))
}
