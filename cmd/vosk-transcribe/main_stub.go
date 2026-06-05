//go:build !cgo

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "vosk-transcribe requires cgo enabled")
	os.Exit(1)
}
