# Tasks for the pvqa-v2 agent: synthetic-speech cues per stream

**Date:** 2026-11-13. **For:** the agent working in `/home/dev/works/pvqa-v2`.
**Written by:** the vq-db agent, from a design study; none of it is implemented, and
nothing below was tested on audio.

## Goal

Find out whether PVQA can tell synthetic (TTS) speech from human speech on telephone
audio, per stream, and if it can, deliver it: a score in the PVQA report that vq-core
passes on to vq-db. The first half is an experiment with a go / no-go result. Do not
start the C++ work on the score (P4) before that result is in and the user has seen it;
the user confirmed this gate. The speech / music segment export (P5) is confirmed
separately and is not behind the gate: do it first.

Read first:

1. `/home/dev/works/vq-db/docs/AASIST-L.md` - the options, the published accuracy of
   AASIST-L and its measured CPU cost.
2. `/home/dev/works/vq-db/docs/SMOOTH_SPEECH.md` - the regularity cues to try.
3. `/home/dev/works/vq-db/docs/TURN_TAKING.md`, section 5 - why music has to be excluded.
4. `/home/dev/works/vq-db/docs/TASKS_VQ_CORE.md` - the parallel work; its tasks C7 and C8
   consume P4 and P5 here.

## What is known

- **The acoustic evidence is weak after telephone codecs.** Narrowband and ACELP codecs
  remove much of what detectors rely on, and the best open systems exceed 10% equal error
  rate on unseen data. Expect a modest result and report it as it is.
- **"Too smooth" detects scripted speech, not synthesis.** A person reading a script
  scores as smooth too. An experiment without scripted human speech overstates the result.
- **The repo already has most of the machinery:** the feature vector
  (`ReportFeatures: yes`), the LightGBM training pipeline (`scripts/accuracy/`), corpus
  generators (`scripts/accuracy/enh_corpus/`, `plc_corpus/`), Python prototypes
  (`scripts/accuracy/prototypes/`, including `pause_features.py`), and in the library a
  pitch processor and a voice-activity detector (`source/lib/common/DSPTools/CPitchProcessor`,
  `CVAD`). These were found by listing, not read; check what they really do.

## Constraints

- **MOS must not change.** New detectors must leave every shipped MOS model's output
  bit-identical; the regression gate (`tests/pvqa_ci/run.sh`) must pass unchanged.
- **Detector names are permanent** once a model reads them. Choose them carefully.
- **Corpus licences.** Non-commercial corpora are for evaluation only, never in a training
  table, as with URGENT. Record the licence of every source.
- **Held-out TTS engines.** Every reported number must come from engines, speakers and
  utterances the model did not see in training. A random split overstates the result.
- **Cost.** Report the added CPU per stream for anything that would run in vq-core.
- **No shipped config changes** until the user decides; work in new config files.

## Tasks

Order: P5, then P1-P3, then the gate, then P4 and P6. P7 only on request.

### P1. Corpus

Build a corpus of human and synthetic speech as it sounds on a call.

- **Human, spontaneous:** conversational telephone or meeting speech.
- **Human, scripted:** read speech and recorded prompts. This class is required.
- **Synthetic:** several current TTS engines, at least three open ones that run locally,
  with different voices and languages. The user has an ElevenLabs API key and allows
  its use: ask for it to be provided as an environment variable, never write it into a
  file, a script, a log or a commit. Before generating, check the plan's character quota
  and tell the user the expected usage (a 30-60 s clip is roughly 500-900 characters),
  and check that the plan's terms allow using the output to train and evaluate a
  detector; if that is unclear, keep ElevenLabs clips for evaluation only. Do not sign up
  for other services.
- **ElevenLabs is one engine, not the corpus.** It goes into training or into the
  held-out test set, not both, and the open engines are still required so that results
  on unseen engines can be reported.
- **Channel:** every clip through G.711, AMR-NB, AMR-WB, EVS and Opus at typical bitrates,
  with and without packet loss, at the sample rate vq-core would decode to. Keep the
  clean version too.
- **Length:** clips of 30-60 s of speech, since the cues are distribution statistics.

**Done when:** the generator is a script in `scripts/accuracy/` with a README giving
sources, licences, counts per class / engine / codec and the fixed train / test split by
engine and speaker.

### P2. Feasibility with the existing features

Run `pvqa_utility` with `ReportFeatures: yes` over the corpus and fit a classifier on the
existing feature vector with the existing training pipeline. No new code in the library.

As a baseline, score the same test set with the published AASIST-L checkpoint
(<https://github.com/clovaai/aasist>, commit `a04c986`, MIT, weights in
`models/weights/AASIST-L.pth`, input 64,600 samples at 16 kHz), averaging over windows.

**Report:** equal error rate and AUC per codec and overall, for synthetic against all
human speech and separately against scripted human speech; the features that carry the
result; the same numbers for the AASIST-L baseline.

### P3. Prototype the regularity cues

In `scripts/accuracy/prototypes/`, in Python, the cues of `SMOOTH_SPEECH.md` section 1:
level stability across utterances, pause-length distribution, speaking-rate variation,
pitch statistics, jitter / shimmer, breaths, filled pauses. Add them to the P2 classifier
and measure what each adds, per codec, with an ablation.

**Report:** which cues survive which codecs, and the gain over P2.

### Gate: stop here and report

Give the user the P2 and P3 numbers and a recommendation: which features are worth
porting, whether the result justifies a shipped score at all, and whether retraining
AASIST-L (P7) is worth the cost. **P4 starts only after the user agrees.**

### P4. Detectors and the synthetic-speech model in the library

Port the cues that earned their place as detectors with new `DetectorType`s, and add a
learned model beside the `MOS Model` that reads their features (and NoiseFloor, Reverb
and whatever P2 found useful) and produces a per-stream score.

- The score is 0..1 with the amount of speech it rests on, and is absent below a minimum.
- It is available per report and cumulatively over a stream, since vq-core analyses
  audio in chunks and the cues need 30-60 s.
- It appears in the JSON report and in the C++ API (`sevana::report`), behind a config
  section that is off unless present.
- Music and non-speech intervals (P5) are excluded from the statistics.

**Done when:** the regression gate passes, the C++ scores match the Python prototype on
the test set within a stated tolerance, the added CPU per stream is measured, and the
detectors and the model are documented in `docs/DETECTORS.md` and `docs/MOS_MODEL.md`.

### P5. Speech segments and a speech / music label (confirmed, do first)

Expose, through the report and the C++ API, the speech segments of the analysed audio
(start and end offsets, at 50 ms accuracy or better) and for each a speech / music /
other label. vq-core uses them for turn-taking on streams that send continuous audio and
to keep music out of every statistic.

This task does not depend on the gate. vq-core task C7 waits for it.

**Done when:** accuracy is reported on speech, music, speech over music and noise, after
each codec; the API is documented in `docs/PVQA C++ API.md`.

### P6. Hand-over to vq-core

Describe for the vq-core agent exactly how to read the segments (P5) and, once it
exists, the score (P4) from `sevana::report`: field names, units, when a value is absent,
and how cumulative state is kept across the chunks of one stream. Write the segments part
as soon as P5 is done.

### P7. Retrained AASIST-L (only if the user asks)

If the gate says the hand-made cues are not enough and the user wants the neural model:
retrain AASIST-L on the P1 training set, at 8 kHz for narrowband streams, and evaluate as
in P2. Its cost is known: about 1.15 s of one core per 4 s window in PyTorch, dominated
by the convolutional encoder, so it would be run on a few windows per stream, not
continuously. A native port is a separate decision.

## Out of scope

- Turn-taking and anything that compares two streams (vq-core).
- Transcription.
- Changes to shipped configs or MOS models.
- vq-core integration itself, beyond the hand-over note P6.

## Report back

After P5: the API description for vq-core and the accuracy table. At the gate: corpus
summary with licences, the tables of P2 and P3, and the recommendation. After P4 and P6: what was added and where, the regression-gate result, the
CPU cost per stream, the API description for vq-core, and what remains unvalidated
(in particular, engines and languages not covered by the corpus).

Follow this repo's `AGENTS.md` for build, test and commit rules; do not commit or push
unless the user asks.
