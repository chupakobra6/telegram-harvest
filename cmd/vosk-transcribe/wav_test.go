package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestReadWAVSpec(t *testing.T) {
	data := buildTestWAV(t, 1, 16000, 16, []byte{0, 1, 2, 3})
	spec, err := readWAVSpec(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("readWAVSpec error = %v", err)
	}
	if spec.audioFormat != 1 {
		t.Fatalf("audioFormat = %d, want 1", spec.audioFormat)
	}
	if spec.channelCount != 1 {
		t.Fatalf("channelCount = %d, want 1", spec.channelCount)
	}
	if spec.sampleRate != 16000 {
		t.Fatalf("sampleRate = %d, want 16000", spec.sampleRate)
	}
	if spec.bitsPerSample != 16 {
		t.Fatalf("bitsPerSample = %d, want 16", spec.bitsPerSample)
	}
	if spec.dataSize != 4 {
		t.Fatalf("dataSize = %d, want 4", spec.dataSize)
	}
	if spec.dataOffset <= 0 {
		t.Fatalf("dataOffset = %d, want > 0", spec.dataOffset)
	}
}

func TestReadWAVSpecRejectsMissingDataChunk(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	writeUint32(t, &buf, 4)
	buf.WriteString("WAVE")
	if _, err := readWAVSpec(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("expected error for missing chunks")
	}
}

func buildTestWAV(t *testing.T, channels uint16, sampleRate uint32, bitsPerSample uint16, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	blockAlign := channels * (bitsPerSample / 8)
	byteRate := uint32(blockAlign) * sampleRate
	riffSize := uint32(4 + 8 + 16 + 8 + len(payload))
	buf.WriteString("RIFF")
	writeUint32(t, &buf, riffSize)
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	writeUint32(t, &buf, 16)
	writeUint16(t, &buf, 1)
	writeUint16(t, &buf, channels)
	writeUint32(t, &buf, sampleRate)
	writeUint32(t, &buf, byteRate)
	writeUint16(t, &buf, blockAlign)
	writeUint16(t, &buf, bitsPerSample)
	buf.WriteString("data")
	writeUint32(t, &buf, uint32(len(payload)))
	buf.Write(payload)
	return buf.Bytes()
}

func writeUint16(t *testing.T, buf *bytes.Buffer, value uint16) {
	t.Helper()
	if err := binary.Write(buf, binary.LittleEndian, value); err != nil {
		t.Fatalf("write uint16: %v", err)
	}
}

func writeUint32(t *testing.T, buf *bytes.Buffer, value uint32) {
	t.Helper()
	if err := binary.Write(buf, binary.LittleEndian, value); err != nil {
		t.Fatalf("write uint32: %v", err)
	}
}
