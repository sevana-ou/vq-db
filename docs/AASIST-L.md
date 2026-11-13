# Detecting synthetic (TTS / LLM-agent) speech, and AASIST-L as a candidate model

**Date:** 2026-11-13. **Status:** investigation only. Nothing is implemented in vq-db,
vq-core or pvqa-v2. The AASIST-L CPU cost was measured; accuracy figures are quoted from
published sources, and nothing was tested on telephony audio.

**Question.** vq-core decodes call audio and PVQA reports its impairments. Can the same
audio and reports tell that a party is artificial speech (a TTS voice or an LLM voice
agent)? Is transcribing the audio the way to do it, and is AASIST-L a usable model?

**Answer.** Detection is possible only as a probabilistic "suspected bot" score per call,
suited to trends and alarms and not to blocking single calls. Transcription is the weakest
option. Conversation timing and an acoustic classifier are better. AASIST-L is small
enough to port and its licence allows it, but its published weights are close to chance on
real-world audio and it costs about 28% of a core per stream, so it would have to be
retrained on codec-degraded speech and run on a few windows per stream only.

## 1. Two different questions

| Question | Property of | Detector type |
|---|---|---|
| Is the voice synthetic? | one stream (acoustic) | anti-spoofing classifier, pause / channel cues |
| Is the party an automated agent? | both legs together (behaviour) | turn-taking statistics |

A bot can play recorded human speech, and a human can speak through a voice changer, so
neither answer implies the other. A useful score fuses both.

## 2. Options, ranked by value for cost

| Approach | What it measures | Where it would live | Main weakness |
|---|---|---|---|
| Turn-taking dynamics | reply gap after the other party stops, overlaps, backchannels, reaction to barge-in | vq-core, call level (it already pairs the legs for `CallHealth`) | full-duplex speech-to-speech agents are closing the gap |
| Channel and pause cues | digital silence or perfectly stationary noise in pauses, no reverberation, no breaths, very even level | pvqa-v2 detectors (NoiseFloor and Reverb exist) | weak one by one; a clean headset in a quiet room looks similar |
| Acoustic anti-spoofing model | vocoder artifacts in the waveform | pvqa-v2, a learned model beside the MOS model | telephony codecs destroy much of the evidence |
| Signalling fingerprints | SIP User-Agent, SDP shape, sender packet pacing | vq-core | erased by any SBC or transcoder on the path |
| Transcription | wording, explicit "I am an AI assistant" disclosures | offline, tracked calls only | cost, privacy, weak signal |

### Why not transcription

- **Weak signal.** Telling LLM text from human text is unreliable even on long written
  passages, and call turns are short. It mostly catches explicit disclosures and IVR phrases.
- **Cost.** ASR per stream is far heavier than PVQA and needs a model per language.
- **Privacy.** Transcripts are call content, which changes the legal position of a quality
  monitor.

### Turn-taking

Humans reply in roughly 200 ms and overlap often. Cascaded voice agents (ASR, LLM, TTS)
typically take 1.4-1.7 s with a narrow spread and almost no overlap. This is an inference
from vendor latency figures, not a tested detection method. It does not depend on codec,
language or content. With DTX or silence suppression the talk-spurts can be read from RTP
marker bits and SID frames, without decoding audio, so it could cover every stream and
not only the audio-enabled ones. The gap seen at the probe includes twice the network
delay between the probe and the replying party.

### Acoustic model on telephony audio

- Narrowband 8 kHz codecs were among the hardest conditions in ASVspoof 5.
- ACELP codecs treat the artifacts as noise and suppress them; EVS is reported as the
  hardest speech codec for detectors.
- The best open systems still exceed 10% equal error rate (EER) on out-of-domain data.

If bots are a few percent of calls, a 10% EER means most single-call flags are false. An
acoustic score is therefore one input to a fused score, averaged over 30-60 s of speech
and trained on our own codec-degraded data.

## 3. AASIST-L

AASIST (NAVER, ICASSP 2022) is a raw-waveform anti-spoofing network: a sinc-filter front
end, a RawNet2-style convolutional encoder, and graph-attention layers over spectral and
temporal nodes. AASIST-L is the lightweight variant.

| | AASIST-L | AASIST |
|---|---|---|
| Parameters | 85,306 | 297,866 |
| Weights file | 426 KB | 1.28 MB |
| Input | 64,600 samples at 16 kHz (4.04 s) | same |
| Licence | MIT (NAVER Corp.), with a RawNet2 baseline subcomponent listed in `NOTICE` | same |

Source: <https://github.com/clovaai/aasist>, commit `a04c986`, which ships both weight
files.

### Accuracy (published)

| Test set | AASIST-L | Source |
|---|---|---|
| ASVspoof 2019 LA (in-domain, clean 16 kHz) | 0.99% EER, min t-DCF 0.0309 | official repository |
| In-the-Wild (real recordings, unseen generators) | 44.45% EER | third-party Hugging Face listing |

- Full AASIST scores 0.83% EER on ASVspoof 2019 LA (from memory, not re-checked).
- The full AASIST baseline scored roughly 29% EER on the ASVspoof 5 evaluation set (from
  memory; a search did not confirm it).
- No published AASIST-L figures were found for narrowband or codec-degraded speech.

The published checkpoint is therefore a baseline only. The architecture is the useful part.

### CPU cost (measured)

Reference PyTorch implementation with the published weights, PyTorch 2.13 CPU build, one
thread, AMD EPYC, median of 20 runs after 3 warm-up runs, random input of one window.

| Model | Time per 4.04 s window | Real-time factor | Streams per core, continuous |
|---|---|---|---|
| AASIST-L | 1,149 ms | 0.285 | about 3.5 |
| AASIST | 1,169 ms | 0.289 | about 3.5 |

- **The small parameter count does not reduce the cost.** A profile of AASIST-L puts 68%
  of the time in the convolutions and 18% in max-pooling of the encoder, which runs over
  the full-rate waveform. The graph-attention part that the "L" variant shrinks is
  negligible.
- **Continuous scoring is unnecessary.** Five speech windows (about 20 s) cost roughly
  6 CPU-seconds once per stream, so one core covers about 10 new audio streams per minute.
- **A narrowband retrain should cost about half** (half the samples for the same
  duration). Estimated, not measured.
- **A native port may not be faster.** The Distill-MOS C++ port is about 1.8x slower than
  PyTorch at present, so budget the PyTorch figure or worse until measured.

The cost applies only to streams that already get audio decoding (SIP track list or
`max-streams-audio`), on top of PVQA.

## 4. Suggested path

1. **Offline feasibility, no new code.** Push human speech and current TTS output through
   the codec chain (G.711, AMR, AMR-WB, EVS, Opus, with packet loss), run `pvqa_utility`
   with `ReportFeatures: yes`, and fit a LightGBM classifier on the existing feature
   vector. Score the same set with the published AASIST-L as a baseline.
2. **Turn-taking features in vq-core**, published per call as a score with its evidence,
   like the `CallHealth` warnings. This needs ground-truth bot calls.
3. **Acoustic model in pvqa-v2**, only if step 1 shows separation after the codecs:
   AASIST-L retrained on the step 1 corpus, at 8 kHz where the stream is narrowband.
4. **vq-db.** Store the score, show a chip on the call, add an alarm counter such as the
   share of suspected-bot calls. This part is small.

A bot built to evade detection will get through; the score describes ordinary voice agents.

## Sources

- AASIST repository: <https://github.com/clovaai/aasist>
- AASIST paper: <https://arxiv.org/abs/2110.01200>
- AASIST-L model card with the In-the-Wild result: <https://huggingface.co/SpeechAntiSpoofingBenchmarks/AASIST-L>
- ASVspoof 5 overview: <https://arxiv.org/abs/2601.03944>
- Vicomtech-UGR ASVspoof 5 systems, per-codec results: <https://arxiv.org/abs/2408.10361>
- Deepfake detection under codec and channel degradation: <https://arxiv.org/abs/2504.12423>
- Detection under real-world communication degradations (ACELP, EVS): <https://arxiv.org/abs/2508.01467>
- Codec-robust countermeasures: <https://arxiv.org/abs/2211.06546>
- Voice-agent latency figures (vendor): <https://telnyx.com/resources/voice-ai-delay-causes.md>,
  <https://www.famulor.io/blog/ai-voice-agent-latency-how-fast-your-phone-bot-must-reply>
