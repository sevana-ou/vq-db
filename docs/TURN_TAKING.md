# Live turn-taking analysis for detecting automated voice agents

**Date:** 2026-11-13. **Status:** design only. Nothing is implemented in vq-net, vq-core,
pvqa-v2 or vq-db. The design comes from reading the vq-net and vq-core sources; no
thresholds were tested on real calls. Background and the acoustic side:
[`AASIST-L.md`](AASIST-L.md).

**Question.** Turn-taking (how quickly and how regularly each party replies) is the
cheapest signal that a call party is an automated voice agent. Should PVQA be extended to
measure it, or does it need a new library? Can the score be live, during the call?

**Answer.** Neither. The analysis belongs in the vq-net RTP library, beside the
call-health rules that vq-core already uses, with a live tracker in vq-core. PVQA at most
supplies speech activity for streams that send continuous audio. A live score is feasible
and can update about once a second, but it needs several exchanges to mean anything:
expect the first usable value after roughly 20-40 s of dialogue.

## 1. Where it belongs

### Not inside PVQA

- **PVQA judges one stream.** Its detectors and feature grammar are per stream and per
  interval. Turn-taking is a property of two streams on a shared clock.
- **The cheap path needs no audio.** With DTX or silence suppression the talk-spurts are
  visible at the RTP layer, for every stream and not only the audio-decoded ones.
- **The free build would lose it.** `--no-pvqa` builds have no PVQA library, but
  turn-taking from RTP timing still works there.

### Not a new library

The call-level machinery exists. `CallHealthTracker` (vq-core,
`vq_core_call_health.{h,cpp}`) groups a SIP call's streams and hands their
`StreamSummary` records to the shared rules in vq-net
(`vq_rtp_library/vq_rtp_call_health.{h,cpp}`), which vq_pcap and the desktop app also use.
One-sided silence is judged there already. Turn-taking has the same shape: per-stream
input, call-level output.

## 2. What exists today

| Piece | Where | Use here |
|---|---|---|
| Frame classification without decoding (speech / SID / NO_DATA for AMR-NB, AMR-WB, EVS) | vq-net `vq_rtp_sid_classifier` | source of talk-spurts for DTX streams |
| Per-interval DTX counters | `StreamDecoder::mDtxStats.mChunk` | shows the per-packet hook is already in place |
| Interval reports per stream, every `report-interval` (default 10,200 ms) | vq-core | carrier for stream-level results |
| Call grouping by SIP Call-ID | `CallHealthTracker` | judges only after the BYE, so not live |
| Voice-activity code | pvqa-v2 `source/app/speech_detector/vad` | possible detector for decoded audio (not reviewed) |

The RTP marker bit is not tracked anywhere in `vq_rtp_library` today.

## 3. Design

```
per packet (vq-net)                         per call (vq-core)                   vq-db
SpeechActivityTracker  --segment closed-->  TurnTakingTracker  --TurnTaking-->   store latest per call,
 one per stream                              pairs the two legs    event          UI chip, alarm counter
                                             incremental rules (vq-net)
```

### 3.1 Speech segments per stream (vq-net)

A `SpeechActivityTracker` per stream, fed where the SID classifier is called today. It
turns per-frame speech / non-speech labels into talk-spurt segments (start and end in
epoch ms), with a minimum segment length and a merge gap for short pauses inside a turn.

Sources of the labels, by stream type:

| Stream type | Source | Needs audio decode |
|---|---|---|
| AMR-NB, AMR-WB, EVS with DTX | existing frame classifier | no |
| G.711 or Opus with silence suppression / DTX | RTP marker bit and packet gaps (new) | no |
| Any codec sending continuous audio | energy or voice-activity detector on decoded PCM | yes: audio-enabled streams only |

The third row is the coverage gap: continuous G.711 without silence suppression is common
on SIP trunks. G.711 energy is a table lookup per sample and could be computed without
the full decode path, but that adds work to the capture threads and must be measured
before it is turned on for every stream.

Accuracy rules:

- **Clock.** Both legs are captured at the same probe, so capture time is a common clock.
  Segment edges come from RTP timestamps anchored to capture time (minimum-delay anchor),
  otherwise jitter smears the gaps.
- **Hangover.** DTX keeps sending speech frames for a codec-specific time after speech
  ends (about 160 ms for AMR). It is subtracted from the segment end.
- **Target.** Edges good to about 50 ms: the difference to detect is 200 ms against 1.5 s.

### 3.2 Turn-taking rules (vq-net)

A pure, incremental component next to the call-health rules: no I/O, no clocks, no
threads. Segments from either leg go in; running statistics and a score come out. That
makes it testable offline on recorded calls and usable by vq_pcap and the desktop app.

Statistics, per direction where it applies:

| Statistic | Human conversation | Cascaded voice agent (ASR, LLM, TTS) |
|---|---|---|
| Median reply gap | about 200 ms | typically 1.4-1.7 s |
| Spread of reply gaps | wide | narrow |
| Share of replies under 300 ms | high | near zero |
| Overlap share | frequent | almost none |
| Backchannels (short spurts during the other party's speech) | present | absent |
| Stop time after being interrupted | variable | fixed, abrupt |

The agent column is an inference from vendor latency figures, not a tested detection
method. The output is a score in 0..1 for each party, the number of turns it rests on,
and the statistics themselves as evidence.

The gap seen at the probe includes twice the network delay between the probe and the
replying party, so the two directions are biased differently. The spread of the gaps is
more robust than their absolute value, and the rules should weight it accordingly.

### 3.3 Live tracker (vq-core)

A `TurnTakingTracker`, built like `CallHealthTracker` (state behind a mutex, publish
callback outside it), with two differences:

- **Fed as segments close**, by a callback from the stream's tracker, not by the
  10.2 s interval reports. This is what makes the score live.
- **Publishes on change**, rate-limited to about one event per second per call, once a
  minimum number of turns exists, and a final event at call end.

Streams are attached to a call by the existing SIP correlation, which can arrive after
the RTP starts; segments seen before that are kept per stream and replayed on attach.

### 3.4 Wire format and vq-db

- A new `TurnTaking` event on the bus: call id, timestamp, final flag, and per party the
  stream id, score, turn count and statistics. A separate event is preferred to a new
  `CallHealth` warning code: this is a likelihood that changes during the call, while
  `CallHealth` is a one-time verdict about faults.
- vq-db keeps the latest event per call (and the final one for finished calls), shows a
  chip on active and finished SIP calls, returns the values in `/sip_calls` and
  `/sip_call`, and adds an alarm counter such as the share of suspected-agent calls.
  Alarms should require a minimum turn count.

## 4. How live it can be

| Stage | Delay |
|---|---|
| Segment closes after speech ends | merge gap plus hangover, a few hundred ms |
| Tracker update and publish | up to the rate limit, about 1 s |
| First usable score | several exchanges: roughly 20-40 s of dialogue |

The plumbing is not the limit; the conversation is. The first reply gap alone is weak
evidence, so the score is published from the first turn together with its turn count,
and consumers decide how many turns they need.

The acoustic cues can be fused as they arrive, but they are slower: PVQA's pause and
noise-floor values come on the 10.2 s interval reports, and an AASIST-style model needs
about 20 s of speech (five windows) for a verdict.

## 5. Music and other non-speech audio

Music breaks the analysis wherever it is taken for speech: it appears as one long,
unbroken talk-spurt, which produces false overlaps and bogus reply gaps. This section is
reasoning from how the codecs and the design work, not a result from recordings.

What music looks like to the `SpeechActivityTracker`:

- **DTX codecs (AMR, EVS).** The encoder's voice-activity detector generally treats music
  as active signal, so no SID frames are sent and the segment never closes. Quiet
  passages may make it flicker.
- **Energy detector on decoded audio.** Continuous activity for as long as the music plays.

| Case | Effect on the score | Excluded without audio? |
|---|---|---|
| Ringback or early media before answer | a long segment before any conversation | yes: analysis starts at call establishment (`onCallEstablished`) |
| Hold signalled in SIP (re-INVITE with `sendonly` / `inactive`) | long segment, then a bogus reply gap when talk resumes | yes: the hold period is masked |
| Queue or hold music from a PBX, no signalling | the same, plus the waiting party's "hello?" counted as overlap or backchannel | partly: by segment shape only |
| Background music behind speech | activity never drops, so no turns are found | no |
| IVR prompts mixed with music | scores as automated: correct, but not an LLM agent | no |

The unsignalled case is the worst error. A caller speaking over queue music looks like
overlap and backchannels, which moves the score towards "human" for the wrong reason, and
the gap between the music ending and an agent's greeting is not a reply to anything.

Mitigations, in the order they should be built:

1. **Gate on signalling.** Ignore everything before the call is established and during
   signalled hold.
2. **Reject non-conversational segments by shape.** Speech pauses every few seconds and
   is active roughly half the time; music is active nearly all the time. A segment longer
   than about 20-30 s without a pause is dropped, with the turns that touch it.
3. **Classify speech against music on decoded audio.** For audio-enabled streams a simple
   discriminator (syllable-rate modulation around 4 Hz, pitch continuity) labels each
   segment. Whether pvqa-v2 already has one was not checked.
4. **Use the Opus mode as a hint.** The first payload byte shows whether a frame is coded
   in the speech or the music mode, without decoding. Many VoIP encoders are locked to the
   speech mode, so this helps only sometimes.
5. **Withhold the score when there is too little clean dialogue.** Publish the analysable
   speech time and the turn count, and no score below a minimum.

The same gating applies to the acoustic side: an AASIST-style model and PVQA's pause cues
mean nothing on music, so their windows are taken only from segments accepted as speech.

Background music behind speech stays unsolved without decoding. Those streams are
reported as not analysable, never with a score.

## 6. Limits

- **Only SIP-correlated calls are paired.** RTP streams without a matching call cannot be
  analysed.
- **Continuous-audio streams need decoding** (section 3.1), so coverage there follows the
  SIP track list or `max-streams-audio`.
- **Full-duplex speech-to-speech agents** reply fast and can produce backchannels; the
  gap between them and humans is closing.
- **Not for blocking single calls.** Slow human agents, interpreters and IVR menus all
  produce unusual timing, and music distorts it (section 5). The score is for trends,
  alarms and triage.
- **Multi-party and forked calls** (more than two streams per call) need a pairing rule;
  the first version should score only calls with exactly one stream per direction.

## 7. Open decisions

1. **Where the score is computed.** Recommended: rules in vq-net and the tracker in
   vq-core, so vq_pcap and the desktop app share them. Alternative: publish only the
   segments and compute in vq-db, which is quicker for tuning thresholds but leaves the
   other tools without the feature.
2. **G.711 energy on the capture path** for streams that are not audio-enabled: needs a
   cost measurement first.
3. **Ground truth.** Thresholds need recorded calls with known voice agents and known
   human parties, per codec.

## 8. Order of work

1. `SpeechActivityTracker` for AMR / EVS from the existing classifier, with unit tests.
2. Turn-taking rules as a pure component, with the signalling gate and the segment-shape
   rule of section 5, validated offline on recorded two-leg calls.
3. `TurnTakingTracker` and the `TurnTaking` event in vq-core.
4. vq-db: decode, store, show, alarm.
5. Marker-bit / packet-gap source for G.711 and Opus, then the decoded-audio detector.
6. Fusion with the acoustic score, if `AASIST-L.md` step 1 shows it is worth having.
