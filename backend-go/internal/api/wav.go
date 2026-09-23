// Package api is the read REST API: handlers, serializers, CSV/WAV output, the
// /stats merge and the SPA static fallback. Port of vq_db/api.
package api

import (
	"encoding/binary"
)

const wavHeaderSize = 44

// ConcatWav concatenates stored WAV chunks into one canonical PCM WAV: strip
// each chunk's 44-byte header, append the PCM, and rebuild a single canonical
// header using the first valid chunk's rate/channels/bit depth. Port of
// vq_db/api/wav.py::concat_wav. Returns empty bytes if there is no PCM.
func ConcatWav(chunks [][]byte) []byte {
	var rate uint32
	var channels uint16 = 1
	var bits uint16 = 16
	var pcm []byte

	for _, w := range chunks {
		if len(w) <= wavHeaderSize || string(w[0:4]) != "RIFF" {
			continue
		}
		if rate == 0 {
			channels = binary.LittleEndian.Uint16(w[22:24])
			if channels == 0 {
				channels = 1
			}
			rate = binary.LittleEndian.Uint32(w[24:28])
			bits = binary.LittleEndian.Uint16(w[34:36])
			if bits == 0 {
				bits = 16
			}
		}
		pcm = append(pcm, w[wavHeaderSize:]...)
	}

	if len(pcm) == 0 || rate == 0 {
		return []byte{}
	}

	blockAlign := channels * bits / 8
	byteRate := rate * uint32(blockAlign)
	dataBytes := uint32(len(pcm))

	out := make([]byte, 0, wavHeaderSize+len(pcm))
	out = append(out, "RIFF"...)
	out = appendU32(out, 36+dataBytes)
	out = append(out, "WAVE"...)
	out = append(out, "fmt "...)
	out = appendU32(out, 16)
	out = appendU16(out, 1) // PCM
	out = appendU16(out, channels)
	out = appendU32(out, rate)
	out = appendU32(out, byteRate)
	out = appendU16(out, blockAlign)
	out = appendU16(out, bits)
	out = append(out, "data"...)
	out = appendU32(out, dataBytes)
	out = append(out, pcm...)
	return out
}

func appendU16(b []byte, v uint16) []byte {
	return binary.LittleEndian.AppendUint16(b, v)
}

func appendU32(b []byte, v uint32) []byte {
	return binary.LittleEndian.AppendUint32(b, v)
}
