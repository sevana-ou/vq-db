# vq-db C++ vs Python — equivalence comparison plan

Status: **plan only** (to be executed later). Goal: validate that the Python
rewrite (`source/vq-db-python`) behaves equivalently to the C++ `source/vq_db`
on the same inputs, and that every difference is an *intended* one we can point
to in the design docs (FILTER.md §6, STATS.md, serializer notes).

The two implementations share three contracts (PLAN.md §2): the ZeroMQ bus
(protobuf `Event`), the SQL schema (`scripts/sql`), and the dashboard HTTP API.
So they can be compared along two independent axes:

- **Write path** — feed both the *same* bus events, let each write its *own*
  database, then diff the databases.
- **Read path** — point both at the *same* pre-populated database, fire the
  *same* HTTP requests, then diff the responses.

Run both; they catch different classes of bugs (ingest/persistence vs
query/serialization).

---

## 1. Inputs / fixtures

| Fixture | What | How produced |
|---------|------|--------------|
| **PCAPs** | A handful of representative captures: clean call, lost/illegal packets, multiple codecs (Opus/AMR/G.711), reINVITE, failed call, SRTP (network-MOS-only), and a publish-audio run. | Reuse `pcap/`, `demo/`, and `scripts/` captures; add any missing case. |
| **Bus tape** | The raw ZMQ frames vq-core emits for a given pcap, recorded once so replays are byte-identical and hermetic (no live vq-core needed per run). | New `tools/bus_record.py` (SUB → length-prefixed frame file). |
| **Seed DB** | A SQLite file pre-populated with a known mix of finished streams, SIP events, and stored audio, used for the read-path axis. | Produced by one run of the write path (either implementation), then frozen and committed as a fixture. |

The bus tape is the key to determinism: vq-core's analysis is the
non-deterministic part, so we freeze its output once and compare only the two
vq-db implementations downstream of it.

---

## 2. Write-path comparison

### 2.1 Setup
- Start the **bus replayer** (`tools/bus_replay.py`) as a ZMQ PUB on a chosen
  port, publishing the recorded tape (preserving inter-frame gaps, or as fast as
  possible — both vq-db instances are pure consumers).
- Start **C++ vq-db** with `database.connection = db=/tmp/cmp/cpp.sqlite`,
  `server.zeromq-port` = replayer port, dashboard disabled or on a spare port.
- Start **Python vq-db** with `database.connection = db=/tmp/cmp/py.sqlite`, same
  bus port. (PUB/SUB fans out, so both receive every frame; or run them in two
  passes against the same tape — both are deterministic given identical bytes.)
- Replay the tape; wait for both task queues / pipelines to drain (poll until row
  counts stabilize), then stop both.

### 2.2 Database diff (`tools/db_diff.py`)
Compare the two SQLite files table-by-table with **normalization**, because
auto-increment PKs and insert ordering are implementation details:

| Table | Compare keyed by | Notes |
|-------|------------------|-------|
| `rtpmon_streams` | `link_id` | ignore `stream_id`, `agent_id` (resolve agent via join to `rtpmon_instances`); compare 5-tuple + SIP columns |
| `rtpmon_intervals` | `(link_id, start_timestamp, end_timestamp)` | join to streams for `link_id` |
| `rtpmon_statistics` | `(link_id, start_timestamp, end_timestamp)` | the finished reports |
| `rtpmon_sip_events` | `(event_type, call_id, event_timestamp)` | ignore `id` |
| `rtpmon_audio` | `(link_id, start_timestamp)` | base64-decode and compare WAV bytes |
| `rtpmon_instances` | `agent_id` | ignore `id` |
| `rtpmon_property` | `name` | |

Comparison rules:
- **Floats** compared with tolerance (proto fields are 32-bit; expect
  ~1e-5 relative differences in MOS/jitter/rtt — not a real diff).
- **Allowlisted columns** (see §4) are reported separately as *expected* diffs,
  not failures.
- Rows present in one DB but not the other are hard failures (missing/extra
  persistence) unless explained by an allowlisted rule (e.g. skip-zero-mos
  threshold differences — but both honor the same config, so none expected).

### 2.3 What this catches
Protobuf decoding, IP/SSRC/link_id mapping, the SIP-event column placement,
audio base64 round-trip, skip-zero-mos, stream→DB-id mapping, and the cleanup
logic (run a tape with old timestamps + short lifetimes to exercise purges).

---

## 3. Read-path comparison

### 3.1 Setup
- Both implementations open the **same frozen seed DB** read-only (SQLite
  readers don't conflict). C++ dashboard on e.g. 9126, Python on e.g. 9136.
- No live bus → both have **empty active-stream registries**, so the in-memory
  `/stats` active list is empty on both and the finished/DB portion is fully
  comparable.

### 3.2 Request matrix (`tools/api_compare.py`)
For each endpoint, fire an identical set of requests at both servers and diff the
responses. Minimum matrix:

| Endpoint | Param variations |
|----------|------------------|
| `/instance_list` | — |
| `/server_stats` | — |
| `/summary` | default (note: `core`/live differ — see §4) |
| `/sip_calls` | no filter; `call_id=…`; `start_timestamp`/`end_timestamp`; paging `limit`/`offset` |
| `/sip_call` | each known `call_id` |
| `/sip_events` | no filter; `type=1..4`; time window; paging |
| `/report` | `id`+`time` for several streams |
| `/streamhistory` | `id` for several streams |
| `/audio` | `link_id` with stored audio (assert identical WAV bytes) and one without (404) |
| `/stats` | no filter; `db_filter` sort variations; filter expressions from the FILTER.md §7 corpus; `download_db` (CSV); `download_db&show_detectors` |
| `/track*` | only if a stub control socket is wired on both; otherwise skip (transport-dependent) |

### 3.3 Response diff & normalization
- JSON: parse both, compare structurally. Numbers with float tolerance. Order of
  arrays must match where the query is ordered (sip lists, stats); for unordered
  collections, compare as multisets.
- **Allowlisted fields** (§4) are diffed separately and must match the documented
  expectation (not byte-equality).
- CSV: compare row-sets keyed by `DB_ID`, with float-tolerant numeric cells; the
  `Time_Start` formatting differs (allowlisted) — compare parsed timestamps, not
  strings.

### 3.4 What this catches
SQL query construction (joins, filters, paging, ordering), the filter
mini-language → SQL, JSON shaping, WAV concatenation, and the PVQA decomposition.

---

## 4. Intentional divergences (the allowlist)

These are **expected** differences the rewrite introduced on purpose. The diff
tools must classify them as expected, and the run is still a PASS if *only* these
appear. Each links to where it's documented.

| Area | C++ | Python | Ref |
|------|-----|--------|-----|
| `jitter` column on write | always `0` | real jitter | writer.py note |
| Filter `network_mos` → column | `sevana_rfactor` (bug) | `network_mos` | FILTER.md §6 |
| Filter `duration` → column | `duration_audio` (bug) | `end−start` | FILTER.md §6 |
| Filter `jitter` in SQL | unmapped | mapped | FILTER.md §6 |
| Filter `start_time` unit (SQL) | ms | seconds | FILTER.md §6 |
| `!=` operator | unreachable | supported | FILTER.md §6 |
| In-memory eval type mismatch | throws → exclude | numeric coercion | FILTER.md §4/§6 |
| `/report` `sevana_rfactor` | computed % | computed % (same intent) | should match |
| `/report`,`/streamhistory` `detectors` | flattened keys | `detectors` array | serialize.py |
| `/stats` finished `start_time` | `ms→string` | epoch ms int | serialize.py |
| `ssrc` rendering | `toHex` | `format(x,'x')` | should match |
| `/summary` `core`, `live` | from live instance stats | empty/0 when no bus | expected in read-path |
| SQL parameterization | inlined | bound params | (no observable diff) |
| CSV `Time_Start` format | C++ formatter | ISO-ish UTC | csv_report.py |
| `/stats` finished count subtraction | `getStreamCount() − active` | `getStreamCount()` | STATS.md §2 |

Anything **not** on this list that differs is a real finding to triage.

Open question to resolve before the run: decide whether to also assert exact
parity on the legacy filter-bug cases by running the C++ with the *old* behavior
(it always has it) and just confirming Python differs as documented — i.e. these
are "assert they differ" cases, not "assert they match".

---

## 5. Tooling to build (under `tools/`)

| Script | Purpose |
|--------|---------|
| `bus_record.py` | SUB to vq-core, write length-prefixed protobuf frames to a tape file. |
| `bus_replay.py` | PUB a tape file to a port (optional real-time pacing). |
| `db_diff.py` | Normalized SQLite-vs-SQLite diff with the §2.2 keys, float tolerance, and the §4 allowlist; emits a categorized report. |
| `api_compare.py` | Drive the §3.2 request matrix against two base URLs, normalize, diff, apply the §4 allowlist; emits a report. |
| `run_comparison.sh` | Orchestrate: build/start both vq-db, replay/seed, run the diffs, print PASS/FAIL summary. |

All are test-harness scripts (not shipped in the package); keep them dependency-light
(stdlib + `pyzmq` + `httpx`, which the project already uses).

---

## 6. Pass / fail criteria

- **PASS**: every diff falls under the §4 allowlist (and float tolerance); no
  missing/extra rows on the write path; no missing/extra fields on the read path.
- **FAIL**: any unexplained row, field, value, status code, or content-type
  difference. Each failure is triaged into: (a) a real Python bug to fix, (b) a
  new intended divergence to add to §4 with justification, or (c) a C++ bug the
  rewrite legitimately corrects (document and keep).

Produce a single machine-readable report (JSON) plus a human summary per run, so
results are reviewable and can later gate CI.

---

## 7. Risks & edge cases

- **vq-core nondeterminism**: mitigated by the frozen bus tape; never compare two
  *live* vq-core runs directly.
- **Float precision**: proto `float` is 32-bit; always compare numerics with
  tolerance, never `==`.
- **SIP-call summary aggregation**: the `max(case when …)` fold must match;
  include a call with reINVITEs + a failed call in the fixtures.
- **Audio**: verify the concatenated WAV from `/audio` is byte-identical (header
  + PCM), not just same length.
- **Timezone/locale**: `Time_Start` / date formatting — compare parsed instants.
- **Engine coverage**: primary run on SQLite (the deployment engine); optionally
  repeat the read-path axis against PostgreSQL/MySQL once drivers are installed.
- **Ordering**: only assert array order where the SQL is `ORDER BY`-stable;
  otherwise compare as multisets.

---

## 8. Runbook (once tooling exists)

```sh
# 0. one-time: record a bus tape from a representative pcap
#    (start vq-core on the pcap, run bus_record.py, save tape)

# 1. write path
mkdir -p /tmp/cmp
tools/bus_replay.py --tape calls.tape --port 9201 &
vq-db        --config cmp-cpp.cfg   # db=/tmp/cmp/cpp.sqlite, zeromq-port 9201
python -m vq_db --config cmp-py.cfg # db=/tmp/cmp/py.sqlite,  zeromq-port 9201
#    (wait for drain, stop both)
tools/db_diff.py /tmp/cmp/cpp.sqlite /tmp/cmp/py.sqlite --allowlist allowlist.json

# 2. read path (against the frozen seed DB)
vq-db        --config read-cpp.cfg   # dashboard 9126, read-only seed.sqlite
python -m vq_db --config read-py.cfg # dashboard 9136, read-only seed.sqlite
tools/api_compare.py --cpp http://localhost:9126 --py http://localhost:9136 \
                     --matrix matrix.json --allowlist allowlist.json
```

---

## 9. Later: CI integration

Once the harness is green, wrap `run_comparison.sh` so it can run on a committed
bus tape + seed DB fixture with no live vq-core, and fail the build on any
non-allowlisted diff. This turns the rewrite's equivalence into a regression gate
for future changes to either implementation.
