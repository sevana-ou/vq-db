package api

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func makeWav(pcm []byte, rate uint32, channels, bits uint16) []byte {
	blockAlign := channels * bits / 8
	byteRate := rate * uint32(blockAlign)
	h := make([]byte, 0, 44+len(pcm))
	h = append(h, "RIFF"...)
	h = binary.LittleEndian.AppendUint32(h, 36+uint32(len(pcm)))
	h = append(h, "WAVE"...)
	h = append(h, "fmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16)
	h = binary.LittleEndian.AppendUint16(h, 1)
	h = binary.LittleEndian.AppendUint16(h, channels)
	h = binary.LittleEndian.AppendUint32(h, rate)
	h = binary.LittleEndian.AppendUint32(h, byteRate)
	h = binary.LittleEndian.AppendUint16(h, blockAlign)
	h = binary.LittleEndian.AppendUint16(h, bits)
	h = append(h, "data"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(len(pcm)))
	return append(h, pcm...)
}

func wav8k(pcm []byte) []byte { return makeWav(pcm, 8000, 1, 16) }

func TestEmptyInput(t *testing.T) {
	if len(ConcatWav(nil)) != 0 {
		t.Error("empty input should give empty output")
	}
}

func TestSingleChunkRoundtripsPCM(t *testing.T) {
	pcm := []byte{1, 2, 3, 4}
	out := ConcatWav([][]byte{wav8k(pcm)})
	if string(out[:4]) != "RIFF" || string(out[36:40]) != "data" {
		t.Error("header wrong")
	}
	if !bytes.Equal(out[44:], pcm) {
		t.Errorf("pcm = %v", out[44:])
	}
	if binary.LittleEndian.Uint32(out[24:28]) != 8000 {
		t.Error("rate not preserved")
	}
}

func TestMultipleChunksConcatenatePCM(t *testing.T) {
	out := ConcatWav([][]byte{wav8k([]byte("AAAA")), wav8k([]byte("BBBB")), wav8k([]byte("CCCC"))})
	if !bytes.Equal(out[44:], []byte("AAAABBBBCCCC")) {
		t.Errorf("pcm = %q", out[44:])
	}
	if binary.LittleEndian.Uint32(out[40:44]) != 12 {
		t.Error("data size wrong")
	}
}

func TestNonRiffChunksSkipped(t *testing.T) {
	out := ConcatWav([][]byte{[]byte("not a wav"), wav8k([]byte("DDDD"))})
	if !bytes.Equal(out[44:], []byte("DDDD")) {
		t.Errorf("pcm = %q", out[44:])
	}
}

func TestRateTakenFromFirstChunk(t *testing.T) {
	out := ConcatWav([][]byte{makeWav([]byte("EEEE"), 16000, 1, 16), makeWav([]byte("FFFF"), 8000, 1, 16)})
	if binary.LittleEndian.Uint32(out[24:28]) != 16000 {
		t.Error("rate should be from first chunk")
	}
	if !bytes.Equal(out[44:], []byte("EEEEFFFF")) {
		t.Errorf("pcm = %q", out[44:])
	}
}
