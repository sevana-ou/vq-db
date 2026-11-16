package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
)

// A call recording built from the stored audio of the call's RTP streams: each
// stream laid on the call's timeline by its capture timestamps and the streams
// mixed, as a recorder on the line would have heard them.

// AudioChunk is one stored piece of a stream's decoded audio: a WAV file and
// the capture time (ms) at which its audio ends. vq-core stores StartMs as the
// stream's start in every chunk, so a chunk is placed by its end.
type AudioChunk struct {
	StartMs int64
	EndMs   int64
	Wav     []byte
}

// MixRate is the sample rate of a mixed recording when the stored audio is at a
// multiple of it (vq-core decodes at 48 kHz): enough for speech, a third of the size.
const MixRate = 16000

// MaxMixSeconds bounds a mixed recording; a longer call is refused.
const MaxMixSeconds = 4 * 3600

// ErrMixTooLong is returned for a call longer than MaxMixSeconds.
var ErrMixTooLong = errors.New("call is too long to mix")

// track is one stream's audio as samples from startMs on.
type track struct {
	startMs int64
	rate    int
	pcm     []int16
}

// trackOf lays a stream's chunks one after another from the first one's start.
// A chunk that starts more than 300 ms after the audio so far ends (packets
// lost to the recorder, a restart) is placed where it belongs, with silence
// before it.
func trackOf(chunks []AudioChunk) (track, bool) {
	var t track
	for _, c := range chunks {
		w := c.Wav
		if len(w) <= wavHeaderSize || string(w[0:4]) != "RIFF" {
			continue
		}
		channels := int(binary.LittleEndian.Uint16(w[22:24]))
		rate := int(binary.LittleEndian.Uint32(w[24:28]))
		bits := int(binary.LittleEndian.Uint16(w[34:36]))
		if channels < 1 || rate <= 0 || bits != 16 || (t.rate != 0 && rate != t.rate) {
			continue
		}
		data := w[wavHeaderSize:]
		frames := len(data) / (2 * channels)
		if frames == 0 {
			continue
		}
		durMs := int64(frames) * 1000 / int64(rate)
		if t.rate == 0 {
			t.rate = rate
			t.startMs = c.EndMs - durMs
			if c.StartMs > 0 && c.StartMs < t.startMs && t.startMs-c.StartMs < 1000 {
				t.startMs = c.StartMs // the first chunk starts with the stream
			}
		}
		have := int64(len(t.pcm)) * 1000 / int64(rate)
		if gap := (c.EndMs - durMs) - (t.startMs + have); gap > 300 {
			t.pcm = append(t.pcm, make([]int16, gap*int64(rate)/1000)...)
		}
		for i := 0; i < frames; i++ {
			// first channel only: vq-core's audio is mono
			t.pcm = append(t.pcm, int16(binary.LittleEndian.Uint16(data[i*2*channels:])))
		}
	}
	return t, len(t.pcm) > 0
}

// toRate converts a track to the given rate: averaging for a whole-number
// reduction (which is also the low-pass it needs), linear interpolation otherwise.
func toRate(pcm []int16, from, to int) []int16 {
	if from == to || len(pcm) == 0 {
		return pcm
	}
	if from > to && from%to == 0 {
		k := from / to
		out := make([]int16, len(pcm)/k)
		for i := range out {
			sum := 0
			for _, v := range pcm[i*k : i*k+k] {
				sum += int(v)
			}
			out[i] = int16(sum / k)
		}
		return out
	}
	n := int(int64(len(pcm)) * int64(to) / int64(from))
	out := make([]int16, n)
	for i := range out {
		pos := float64(i) * float64(from) / float64(to)
		j := int(pos)
		if j+1 >= len(pcm) {
			out[i] = pcm[len(pcm)-1]
			continue
		}
		f := pos - float64(j)
		out[i] = int16(float64(pcm[j])*(1-f) + float64(pcm[j+1])*f)
	}
	return out
}

// MixCallAudio mixes the streams of a call into one WAV. Mono: every stream
// summed. Stereo: the first stream (the earliest to start) on the left, the
// second on the right, any further ones alternating. Returns nil when no
// stream has audio.
func MixCallAudio(streams [][]AudioChunk, stereo bool) ([]byte, error) {
	var tracks []track
	for _, chunks := range streams {
		if t, ok := trackOf(chunks); ok {
			tracks = append(tracks, t)
		}
	}
	if len(tracks) == 0 {
		return nil, nil
	}
	// One rate for the mix: MixRate when every stream reduces to it by a whole
	// number, otherwise the lowest stored rate.
	rate := MixRate
	for _, t := range tracks {
		if t.rate < MixRate || t.rate%MixRate != 0 {
			rate = 0
		}
	}
	if rate == 0 {
		rate = tracks[0].rate
		for _, t := range tracks {
			rate = min(rate, t.rate)
		}
	}
	// earliest stream first: it is the left channel
	for i := 1; i < len(tracks); i++ {
		for j := i; j > 0 && tracks[j].startMs < tracks[j-1].startMs; j-- {
			tracks[j], tracks[j-1] = tracks[j-1], tracks[j]
		}
	}
	start := tracks[0].startMs
	total := 0
	offsets := make([]int, len(tracks))
	for i := range tracks {
		tracks[i].pcm = toRate(tracks[i].pcm, tracks[i].rate, rate)
		offsets[i] = int((tracks[i].startMs - start) * int64(rate) / 1000)
		total = max(total, offsets[i]+len(tracks[i].pcm))
	}
	if total > MaxMixSeconds*rate {
		return nil, ErrMixTooLong
	}
	channels := 1
	if stereo {
		channels = 2
	}
	mix := make([]int32, total*channels)
	for i, t := range tracks {
		ch := 0
		if stereo {
			ch = i % 2
		}
		for k, v := range t.pcm {
			mix[(offsets[i]+k)*channels+ch] += int32(v)
		}
	}
	out := make([]byte, 0, wavHeaderSize+len(mix)*2)
	out = append(out, "RIFF"...)
	out = appendU32(out, uint32(36+len(mix)*2))
	out = append(out, "WAVEfmt "...)
	out = appendU32(out, 16)
	out = appendU16(out, 1)
	out = appendU16(out, uint16(channels))
	out = appendU32(out, uint32(rate))
	out = appendU32(out, uint32(rate*channels*2))
	out = appendU16(out, uint16(channels*2))
	out = appendU16(out, 16)
	out = append(out, "data"...)
	out = appendU32(out, uint32(len(mix)*2))
	for _, v := range mix {
		out = appendU16(out, uint16(int16(max(-32768, min(32767, v)))))
	}
	return out, nil
}

// GetCallAudioChunks returns the stored audio of every stream of a call, one
// list per stream, each in capture order.
func GetCallAudioChunks(db *sql.DB, callID string) ([][]AudioChunk, error) {
	rows, err := db.Query("select rtpmon_audio.stream_id, rtpmon_audio.start_timestamp, rtpmon_audio.end_timestamp,"+
		" rtpmon_audio.audio_wav from rtpmon_audio"+
		" inner join rtpmon_streams on rtpmon_streams.stream_id = rtpmon_audio.stream_id"+
		" where rtpmon_streams.sip_callid = ?"+
		" order by rtpmon_audio.stream_id, rtpmon_audio.end_timestamp, rtpmon_audio.id", callID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]AudioChunk
	last := int64(-1)
	for rows.Next() {
		var id, start, end int64
		var encoded sql.NullString
		if err := rows.Scan(&id, &start, &end, &encoded); err != nil {
			return nil, err
		}
		wav, err := base64.StdEncoding.DecodeString(encoded.String)
		if err != nil || len(wav) == 0 {
			continue
		}
		if id != last {
			out = append(out, nil)
			last = id
		}
		out[len(out)-1] = append(out[len(out)-1], AudioChunk{StartMs: start, EndMs: end, Wav: wav})
	}
	return out, rows.Err()
}
