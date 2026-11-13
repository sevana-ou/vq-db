# Tasks for the vq-core agent: live turn-taking score

**Date:** 2026-11-13. **For:** the agent working in `/home/dev/works/vq-core` (the work
also changes the shared code in `/home/dev/works/vq-net/source`). **Written by:** the
vq-db agent, from a design study; none of it is implemented and the code references below
were found by reading and grepping, not by building.

## Goal

Publish, during a call, a score saying how likely each party is an automated voice agent,
judged from conversation timing: how fast and how regularly each side replies. vq-db
will store it, show it on the call and raise alarms on it.

Read first, in this order:

1. `/home/dev/works/vq-db/docs/TURN_TAKING.md` - the design. It is the specification for
   this work; where this file and the design disagree, say so and follow the design.
2. `/home/dev/works/vq-db/docs/AASIST-L.md`, sections 1-2 - why turn-taking, and what the
   other signals are.
3. `/home/dev/works/vq-db/docs/TASKS_PVQA_V2.md` - the parallel work in pvqa-v2. Tasks C7
   and C8 here depend on it.

## What is known about the code

- **Two per-packet paths.** Network-only streams run through the allocation-free
  `StreamParser` (`source/vq_core/vq_core_stream_parser.cpp`, default on); audio-enabled
  streams are promoted to `StreamDecoder` (vq-net
  `vq_rtp_library/vq_rtp_stream_decoder.cpp`). Both already call `classifyAmr` /
  `classifyEvs` per packet (parser near line 285, decoder near line 339). Speech activity
  has to work in both and survive the promotion from one to the other.
- **The marker bit is parsed** into `RtpHeaderView::mMarker`
  (`vq_core_rtp_header_view.h`) but nothing uses it for talk-spurts.
- **Call grouping exists** in `CallHealthTracker` (`vq_core_call_health.{h,cpp}`), wired
  in `protobuf/report-producer.cpp`. It judges only after the BYE. Its rules live in vq-net
  `vq_rtp_library/vq_rtp_call_health.{h,cpp}` and are shared with vq_pcap and the desktop
  app.
- **Hold is not detected.** No `sendonly` / `inactive` handling was found in
  `vq_rtp_sip_manager`.
- **Interval reports come every 10,200 ms** by default (`DEFAULT_REPORT_INTERVAL`), which
  is too slow to carry a live score.

## Constraints

- **The `StreamParser` path stays allocation-free per packet** and its performance gate
  (`STREAM-PARSER.md`, `PERFORMANCE.md`) must still pass. Measure before and after.
- **The free build must keep working.** Turn-taking from RTP timing must build and run
  with `--no-pvqa`; anything that needs PVQA goes under `USE_PVQA_LIBRARY`.
- **Rules are pure.** The turn-taking rules take segments and return statistics: no I/O,
  no clocks, no threads, so they can be unit-tested and reused by vq_pcap.
- **Off by default** behind a config switch until thresholds are validated on real calls
  (confirmed by the user).
- **The score is computed here**, in vq-core with the rules in vq-net (confirmed by the
  user). Do not move the computation to vq-db.
- **The wire contract is additive.** New protobuf fields and messages only; existing
  field numbers do not change.

## Tasks

Do them in order. C1-C3 are testable without a running daemon.

### C1. Speech segments for DTX codecs (vq-net)

A `SpeechActivityTracker` in `vq_rtp_library`, one per stream, fed with the per-frame
class (speech / SID / NO_DATA) that the classifier already produces, plus the frame's
time. It emits closed talk-spurt segments (start, end in epoch ms) and exposes the start
of a spurt still open.

- Segment edges use RTP timestamps anchored to capture time, so jitter does not move them.
- The codec's DTX hangover is subtracted from the segment end (design section 3.1).
- Minimum segment length and merge gap are settings with documented defaults.
- Hook it into both `StreamParser` and `StreamDecoder`; carry its state across promotion.

**Done when:** unit tests cover AMR-NB, AMR-WB and EVS frame sequences including loss,
reordering and a promotion mid-spurt; edges are within 50 ms of the truth in the tests;
the parser path allocates nothing per packet.

### C2. Speech segments for G.711 and Opus without decoding (vq-net)

A second source for the same tracker: the RTP marker bit and gaps in the packet sequence
for streams using silence suppression, and Opus DTX. A stream that sends continuous audio
must be recognised as such and reported as "no activity information", not as one endless
talk-spurt.

**Done when:** unit tests cover G.711 with and without silence suppression and Opus with
and without DTX; continuous streams yield no segments and a flag saying why.

### C3. Turn-taking rules (vq-net)

A pure, incremental component beside the call-health rules. Input: segments of the two
directions of one call, in any arrival order. Output per party: the statistics of design
section 3.2 (median and spread of reply gaps, share of replies under 300 ms, overlap
share, backchannel count, stop time after interruption), the number of turns, the
analysable speech time and a score in 0..1.

- Include the segment-shape rule of design section 5: a segment longer than about
  20-30 s without a pause is treated as music or announcement and dropped together with
  the turns touching it.
- Below a minimum of clean dialogue the score is absent, not zero.
- Every threshold is a field of a settings struct, as in `call_health::Settings`.
- The mapping from statistics to score is a first guess. Keep it in one function and
  document it, because it will be re-fitted once labelled calls exist (C9).

**Done when:** unit tests cover a human-like dialogue, an agent-like dialogue (1.5 s
steady gaps, no overlap), music on one leg, a one-sided call and segments arriving out of
order. Also provide an offline way to print the statistics for a `.pcap` (the replay
path, `analyzePcapFile`), since that is how thresholds will be tuned.

### C4. Live tracker (vq-core)

A `TurnTakingTracker` modelled on `CallHealthTracker` (state behind a mutex, publish
callback outside it).

- Fed by a callback when a segment closes, not by interval reports.
- Streams are attached to a call by the existing SIP correlation. Segments seen before the
  correlation arrives are kept per stream, bounded, and replayed on attach.
- Analysis starts at call establishment; ringback and early media are ignored.
- Publishes on change, at most about once per second per call, and a final result at call
  end. Calls that never reach the minimum publish nothing.
- First version: only calls with exactly one stream per direction are scored. Others are
  skipped, with a counter.

**Done when:** a pcap replay of a two-party call produces a sequence of updates and one
final result; memory per call is bounded and freed at call end or after the stale
timeout; a pending-calls gauge exists, as `CallHealthTracker::pending()`.

### C5. Hold detection (vq-net SIP manager, vq-core)

Detect signalled hold from the SDP direction attribute in re-INVITE / UPDATE
(`sendonly`, `inactive`, and the legacy `c=0.0.0.0`) and mask that period in the tracker,
so hold music produces neither segments nor a reply gap when talk resumes.

**Done when:** a unit or replay test with a hold and resume shows no turns across the
hold.

### C6. Wire format and configuration

A new `TurnTaking` message and an `Event` field for it in
`source/protobuf/capturemessage.proto`, with marshalling. The user approved this shape;
choose the `Event` field number, and keep the names and numbers below unless there is a
reason not to (report any change):

```proto
message TurnTaking {
    string call_id      = 1;   // SIP Call-ID
    uint64 timestamp    = 2;   // evaluation time, UNIX ms
    bool   final        = 3;   // last result for this call

    message Party {
        string stream_id            = 1;   // StreamId.uuid of the stream this party SENDS
        bool   has_score            = 2;
        float  score                = 3;   // 0..1, likelihood of an automated agent
        uint32 turns                = 4;   // replies by this party the score rests on
        uint32 speech_ms            = 5;   // analysable speech time
        float  reply_gap_median_ms  = 6;
        float  reply_gap_iqr_ms     = 7;
        float  fast_reply_share     = 8;   // replies under 300 ms
        float  overlap_share        = 9;
        uint32 backchannels         = 10;
        float  barge_in_stop_ms     = 11;  // 0 = not observed
        string not_analysable       = 12;  // empty, or a short reason ("continuous audio", "music"...)
    }
    repeated Party parties = 4;
}
```

Configuration in `vq-monitor.cfg`: an enable switch (default off), the minimum turns
before publishing, the publish rate limit and the rule thresholds. Document the keys
where the other server keys are documented.

**Done when:** the event is on the bus during a replay, and the final `.proto` text of
the new message and `Event` field is in the report back, so vq-db can copy it exactly.

### C7. Speech activity from decoded audio (depends on pvqa-v2 P5)

For audio-enabled streams that send continuous audio, feed the tracker from the decoded
PCM on the `DecodeWorker`, using the speech segments and speech / music labels that
pvqa-v2 task P5 provides (the user confirmed P5; it is done first in pvqa-v2). In
`--no-pvqa` builds this source is simply absent.

Separately, **measure only**: the cost of computing G.711 frame energy on the capture path
for streams that are not audio-enabled (a table lookup per sample). Report CPU per 1,000
streams and the effect on drops. Do not enable it: the user confirmed cost measurement
only.

### C8. Carry the synthetic-speech score (depends on pvqa-v2 P4)

When the PVQA report carries the per-stream synthetic-speech score, pass it through in
the stream report message as new optional fields (score, speech seconds it rests on).
Report the final field names and numbers back.

### C9. Ground truth (later, with the user)

Thresholds cannot be validated without calls whose parties are known. The user will
generate recordings of calls with voice agents later; do not wait for them. Build C1-C8
with the first-guess mapping, make re-fitting it easy (the offline pcap tool of C3), and
state clearly in the report that the score mapping is unvalidated. When the recordings
arrive: run them and known human calls through the tool, per codec, re-fit the mapping
and report the error rates.

Suggested source of the voice-agent calls: the user's ElevenLabs account (Creator plan).
According to ElevenLabs' agents pricing page, checked 2026-11-13, it includes the agent
product with 275 call minutes a month and up to 10 concurrent calls, billed separately
from the text-to-speech credits; extra minutes cost $0.08, and the LLM the agent uses is
billed on top. That is about 90 three-minute calls a month. Open points for the user to
check in the account: whether a SIP trunk can be connected on this plan, and what the
external trunk provider charges. The calls must pass the probe as SIP + RTP so both legs
are captured; record them per codec, with a human on the other leg, and keep the pcaps
with a note of which party is the agent. Any API key is given as an environment
variable and never written to a file, a log or a commit.

## Out of scope

- The acoustic model and the "too smooth" detectors (pvqa-v2).
- Anything in vq-db: storage, API, UI and alarms follow once C6 is on the bus.
- Calls with more than two streams, conferences and forked calls.
- Fusing the turn-taking score with the acoustic scores; vq-db will do that.

## Report back

For each task: what was done and where, the tests added and their result, and any
deviation from the design with the reason. Also:

- the final protobuf text (C6, C8) and the config keys with defaults;
- the performance numbers for the `StreamParser` path before and after;
- the G.711 energy measurement (C7);
- which codecs and stream types produce segments, and which are reported as not analysable;
- what remains unvalidated.

Follow `AGENTS.md` of vq-core and of vq-net for build, test and commit rules; do not
commit or push unless the user asks.
