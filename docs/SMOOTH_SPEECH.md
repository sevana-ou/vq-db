# Detecting "too smooth" speech

**Date:** 2026-11-13. **Status:** idea only. Nothing is implemented or prototyped, and
none of the cues below was tested on our audio; they come from general knowledge of
speech analysis. Related: [`AASIST-L.md`](AASIST-L.md) (acoustic model, overall plan) and
[`TURN_TAKING.md`](TURN_TAKING.md) (call-level timing).

**Question.** Synthetic speech often sounds too even: constant level, regular pauses, no
hesitation. Can that be detected on a call stream?

**Answer.** Partly. "Too smooth" can be measured as a set of regularity statistics on one
stream, and the most useful of them survive telephone codecs. But they separate
spontaneous speech from scripted or produced speech, not human from machine, so the
result is a supporting cue for a fused score and not proof of synthesis. Being a
per-stream property, it fits PVQA.

## 1. Measurable cues

| Cue | Human spontaneous speech | Typical TTS | Survives telephone codecs? |
|---|---|---|---|
| Loudness across utterances | drifts with distance and effort | nearly constant | yes, but handset gain control also flattens it |
| Pause lengths inside a turn | irregular, with hesitations mid-phrase | clustered at a few fixed lengths, placed at punctuation | yes |
| Speaking rate over the call | speeds up and slows down | steady | yes |
| Pitch range and contour | wide, uneven | natural-sounding in modern engines, but repeats similar patterns | mostly |
| Cycle-to-cycle pitch and amplitude variation (jitter, shimmer) | always present | often too regular | poorly: AMR and EVS re-synthesise the excitation |
| Breaths before phrases | audible | usually absent | poorly: handset noise suppression removes them too |
| Fillers and restarts ("uh", false starts) | frequent | absent unless added on purpose | yes |
| Room and handling noise | changes during the call | none, or a perfectly steady bed | yes; PVQA's NoiseFloor and Reverb cover this |

The robust cues are loudness, pause lengths, speaking rate and fillers: they depend on
timing and level, which codecs preserve. Filled pauses can be found acoustically, without
transcription, as a long vowel with flat pitch and stable formants.

## 2. What it cannot tell

- **It separates spontaneous from scripted, not human from machine.** A call-centre agent
  reading a script, a recorded announcement or a trained speaker scores as smooth too.
- **Voice agents are adapting.** Current agents insert fillers and breaths on purpose.
  Regularity over the whole call (the same pause lengths, rate and level for minutes) is
  harder to fake than any single local cue, so long-range statistics should carry the
  weight.
- **It needs time.** These are distribution statistics; expect 30-60 s of speech from one
  party before they are stable.
- **Music and non-speech audio** must be excluded first, as for turn-taking
  (`TURN_TAKING.md`, section 5).

## 3. Where it fits

- **New detectors in pvqa-v2** for level stability, pause-length distribution,
  syllable-rate variation, pitch statistics and filled pauses. Whether pvqa-v2 already has
  a pitch tracker to build on was not checked.
- **A learned model beside the MOS model**, reading those features together with the
  existing NoiseFloor and Reverb values. The feature grammar and the LightGBM machinery
  exist already.
- **Live updates** come with each interval report (every 10.2 s by default) and
  accumulate over the call. Only audio-enabled streams are covered (SIP track list or
  `max-streams-audio`).
- **Decoded audio is required for the pause statistics.** With DTX alone, pauses shorter
  than the codec hangover (about 160 ms for AMR) are invisible.

The stream-level result is one input to the fused per-call score, next to the turn-taking
score and the acoustic model.

## 4. First step

The cues are cheap to try in the offline feasibility step of `AASIST-L.md` (section 4,
step 1): compute them on human and TTS speech passed through the codec chain and see
which separate. Include scripted human speech (read text, recorded prompts) in the human
set, otherwise the test measures spontaneity and overstates the result.
