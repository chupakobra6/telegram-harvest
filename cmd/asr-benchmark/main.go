package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/asrbench"
)

func main() {
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("asr-benchmark", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "", "benchmark manifest JSON")
	outputPath := fs.String("out", "", "result JSON")
	runs := fs.Int("runs", 3, "cold process repetitions per variant")
	variantName := fs.String("variant", "", "optional exact variant name")
	timeout := fs.Duration("timeout", 30*time.Minute, "overall timeout")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if *manifestPath == "" || *outputPath == "" {
		fmt.Fprintln(os.Stderr, "--manifest and --out are required")
		return 2
	}
	manifest, err := asrbench.LoadManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *variantName != "" {
		filtered := manifest.Variants[:0]
		for _, variant := range manifest.Variants {
			if variant.Name == *variantName {
				filtered = append(filtered, variant)
			}
		}
		manifest.Variants = filtered
		if len(manifest.Variants) == 0 {
			fmt.Fprintf(os.Stderr, "variant %q not found\n", *variantName)
			return 2
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	workDir := filepath.Join(filepath.Dir(*outputPath), "work")
	report, err := asrbench.Run(ctx, manifest, *runs, workDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	temp, err := os.CreateTemp(filepath.Dir(*outputPath), ".asr-benchmark-*.tmp")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(append(content, '\n')); err != nil {
		_ = temp.Close()
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := temp.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.Rename(tempPath, *outputPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("benchmark=%s corpus=%s samples=%d audio=%.3fs variants=%d runs=%d out=%s\n",
		report.Name, report.CorpusHash, report.Samples, report.AudioSeconds, len(report.Variants), report.Runs, *outputPath)
	return 0
}
