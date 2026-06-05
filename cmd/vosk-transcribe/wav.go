package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

type wavSpec struct {
	audioFormat   uint16
	channelCount  uint16
	sampleRate    uint32
	bitsPerSample uint16
	dataOffset    int64
	dataSize      int64
}

type wavReader struct {
	file       *os.File
	sampleRate float64
	remaining  int64
}

func openPCM16MonoWAV(path string) (*wavReader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	spec, err := readWAVSpec(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	if spec.audioFormat != 1 {
		file.Close()
		return nil, fmt.Errorf("unsupported wav format: expected PCM")
	}
	if spec.channelCount != 1 || spec.bitsPerSample != 16 {
		file.Close()
		return nil, fmt.Errorf("unsupported wav format: expected mono 16-bit PCM")
	}
	if _, err := file.Seek(spec.dataOffset, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	return &wavReader{
		file:       file,
		sampleRate: float64(spec.sampleRate),
		remaining:  spec.dataSize,
	}, nil
}

func (r *wavReader) SampleRate() float64 {
	return r.sampleRate
}

func (r *wavReader) ReadChunk(buf []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(buf)) > r.remaining {
		buf = buf[:int(r.remaining)]
	}
	n, err := r.file.Read(buf)
	r.remaining -= int64(n)
	if err == io.EOF && n > 0 {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	if r.remaining == 0 {
		return n, io.EOF
	}
	return n, nil
}

func (r *wavReader) Close() error {
	return r.file.Close()
}

func readWAVSpec(r io.ReadSeeker) (wavSpec, error) {
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil {
		return wavSpec{}, err
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return wavSpec{}, fmt.Errorf("unsupported wav format: expected RIFF/WAVE")
	}
	var (
		spec    wavSpec
		gotFmt  bool
		gotData bool
		header  [8]byte
	)
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return wavSpec{}, err
		}
		chunkID := string(header[0:4])
		chunkSize := int64(binary.LittleEndian.Uint32(header[4:8]))
		chunkDataOffset, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return wavSpec{}, err
		}

		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return wavSpec{}, fmt.Errorf("unsupported wav format: invalid fmt chunk")
			}
			data := make([]byte, chunkSize)
			if _, err := io.ReadFull(r, data); err != nil {
				return wavSpec{}, err
			}
			spec.audioFormat = binary.LittleEndian.Uint16(data[0:2])
			spec.channelCount = binary.LittleEndian.Uint16(data[2:4])
			spec.sampleRate = binary.LittleEndian.Uint32(data[4:8])
			spec.bitsPerSample = binary.LittleEndian.Uint16(data[14:16])
			gotFmt = true
		case "data":
			spec.dataOffset = chunkDataOffset
			spec.dataSize = chunkSize
			if _, err := r.Seek(chunkSize, io.SeekCurrent); err != nil {
				return wavSpec{}, err
			}
			gotData = true
		default:
			if _, err := r.Seek(chunkSize, io.SeekCurrent); err != nil {
				return wavSpec{}, err
			}
		}

		if chunkSize%2 == 1 {
			if _, err := r.Seek(1, io.SeekCurrent); err != nil {
				return wavSpec{}, err
			}
		}
		if gotFmt && gotData {
			return spec, nil
		}
	}
	if !gotFmt || !gotData {
		return wavSpec{}, fmt.Errorf("unsupported wav format: missing fmt or data chunk")
	}
	return spec, nil
}
