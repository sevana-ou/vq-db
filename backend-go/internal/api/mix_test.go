package api

import (
	"encoding/binary"
	"testing"
)

// wavOf builds a mono 16-bit WAV of n samples, all of value v.
func wavOf(rate, n int, v int16) []byte {
	out := []byte("RIFF")
	out = appendU32(out, uint32(36+n*2))
	out = append(out, "WAVEfmt "...)
	out = appendU32(out, 16)
	out = appendU16(out, 1)
	out = appendU16(out, 1)
	out = appendU32(out, uint32(rate))
	out = appendU32(out, uint32(rate*2))
	out = appendU16(out, 2)
	out = appendU16(out, 16)
	out = append(out, "data"...)
	out = appendU32(out, uint32(n*2))
	for i := 0; i < n; i++ {
		out = appendU16(out, uint16(v))
	}
	return out
}

func samples(t *testing.T, wav []byte) (rate, channels int, pcm []int16) {
	t.Helper()
	if len(wav) < wavHeaderSize || string(wav[:4]) != "RIFF" {
		t.Fatalf("not a WAV: %d bytes", len(wav))
	}
	channels = int(binary.LittleEndian.Uint16(wav[22:24]))
	rate = int(binary.LittleEndian.Uint32(wav[24:28]))
	if got := int(binary.LittleEndian.Uint32(wav[40:44])); got != len(wav)-wavHeaderSize {
		t.Fatalf("data size %d, have %d", got, len(wav)-wavHeaderSize)
	}
	for i := wavHeaderSize; i+1 < len(wav); i += 2 {
		pcm = append(pcm, int16(binary.LittleEndian.Uint16(wav[i:])))
	}
	return
}

func TestMixCallAudioPlacesStreamsOnOneTimeline(t *testing.T) {
	// vq-core's layout: every chunk carries the stream's start, and ends where its audio ends.
	// A: 2 s from t=10 s, in two chunks. B: 1 s starting half a second later.
	a := []AudioChunk{{StartMs: 10000, EndMs: 11000, Wav: wavOf(48000, 48000, 1000)},
		{StartMs: 10000, EndMs: 12000, Wav: wavOf(48000, 48000, 1000)}}
	b := []AudioChunk{{StartMs: 10500, EndMs: 11500, Wav: wavOf(48000, 48000, 300)}}

	wav, err := MixCallAudio([][]AudioChunk{b, a}, false)
	if err != nil {
		t.Fatal(err)
	}
	rate, ch, pcm := samples(t, wav)
	if rate != MixRate || ch != 1 || len(pcm) != 2*MixRate {
		t.Fatalf("rate %d channels %d samples %d", rate, ch, len(pcm))
	}
	at := func(ms int) int16 { return pcm[ms*MixRate/1000] }
	if at(100) != 1000 || at(700) != 1300 || at(1400) != 1300 || at(1700) != 1000 {
		t.Errorf("mix at 0.1 / 0.7 / 1.4 / 1.7 s = %d %d %d %d", at(100), at(700), at(1400), at(1700))
	}

	// Stereo: the stream that starts first is the left channel.
	wav, _ = MixCallAudio([][]AudioChunk{b, a}, true)
	_, ch, pcm = samples(t, wav)
	if ch != 2 || len(pcm) != 4*MixRate {
		t.Fatalf("stereo: channels %d samples %d", ch, len(pcm))
	}
	i := 700 * MixRate / 1000 * 2
	if pcm[i] != 1000 || pcm[i+1] != 300 {
		t.Errorf("stereo at 0.7 s: left %d right %d", pcm[i], pcm[i+1])
	}
	if j := 100 * MixRate / 1000 * 2; pcm[j] != 1000 || pcm[j+1] != 0 {
		t.Errorf("stereo at 0.1 s: left %d right %d", pcm[j], pcm[j+1])
	}
}

func TestMixCallAudioGapClipAndRates(t *testing.T) {
	// A gap in a stream (its second chunk ends 3 s after the first, with 1 s of audio) is silence.
	gap := []AudioChunk{{StartMs: 0, EndMs: 1000, Wav: wavOf(8000, 8000, 500)},
		{StartMs: 0, EndMs: 4000, Wav: wavOf(8000, 8000, 500)}}
	wav, _ := MixCallAudio([][]AudioChunk{gap}, false)
	rate, _, pcm := samples(t, wav)
	if rate != 8000 || len(pcm) != 4*8000 || pcm[500*8] != 500 || pcm[2000*8] != 0 || pcm[3500*8] != 500 {
		t.Errorf("gap: rate %d, %d samples, values %d %d %d", rate, len(pcm), pcm[500*8], pcm[2000*8], pcm[3500*8])
	}

	// Two loud streams do not wrap around.
	loud := []AudioChunk{{StartMs: 0, EndMs: 1000, Wav: wavOf(16000, 16000, 30000)}}
	wav, _ = MixCallAudio([][]AudioChunk{loud, loud}, false)
	if _, _, pcm := samples(t, wav); pcm[100] != 32767 {
		t.Errorf("clipping: %d", pcm[100])
	}

	// Streams at different rates meet at the lower one.
	wav, _ = MixCallAudio([][]AudioChunk{
		{{StartMs: 0, EndMs: 1000, Wav: wavOf(8000, 8000, 100)}},
		{{StartMs: 0, EndMs: 1000, Wav: wavOf(48000, 48000, 200)}}}, false)
	if rate, _, pcm := samples(t, wav); rate != 8000 || len(pcm) != 8000 || pcm[4000] != 300 {
		t.Errorf("mixed rates: rate %d, %d samples, value %d", rate, len(pcm), pcm[4000])
	}

	// Nothing usable: no recording, and no error.
	if wav, err := MixCallAudio([][]AudioChunk{{{Wav: []byte("junk")}}, nil}, false); wav != nil || err != nil {
		t.Errorf("no audio: %d bytes, %v", len(wav), err)
	}
}
