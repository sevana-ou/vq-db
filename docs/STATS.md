# `/stats` endpoint — deep dive

Companion to `PLAN.md` (Phase 3). `/stats` is the most intricate dashboard
endpoint: it returns **two** paged stream lists in one response — *active*
(in-memory, in-progress streams) and *finished* (from the DB) — each with its own
filter, sort, paging, and counts.

Source of truth (C++):
- Handler / flags / counts: `source/vq_db/dashboard/dashboard_api.cpp`
  `StatisticsHandler::handleRequest` (~L42-258).
- List pipelines: `source/vq_db/dashboard/dashboard_data.cpp`
  `StreamList` / `InMemoryList` / `DbList` (~L317-625).
- DB query assembly: `source/vq_db/database.cpp` `makeStreamSearch` (~L1630-1860).

---

## 1. Request parameters

Two parallel filter strings, each in the `SearchFilter` transport format
(`field/dir/offset/count/<b64expr>`; see FILTER.md):

| Param | Applies to |
|-------|-----------|
| `mem_filter` | active list |
| `db_filter` | finished list |
| `action` | repaging command, prefixed `mem_` or `db_` (see §2) |
| `mem_start_timestamp` / `mem_end_timestamp` | active date window (seconds) |
| `db_start_timestamp` / `db_end_timestamp` | finished date window (seconds) |
| `sip_callid` | exact Call-ID, applied to **both** lists (jump call → streams) |

Output-selection flags (all boolean unless noted):

| Flag | Effect |
|------|--------|
| `show_active` | include `active_streams` + active counts |
| `show_finished` | include `finished_streams` + finished counts |
| `mem_show_count` | include `available_active_streams_count` only |
| `db_show_count` | include `available_finished_streams_count` only |
| `download_db` | stream the finished list as CSV (`text/csv`, all rows) |
| `download_active` | stream the active list as CSV (all rows, with detectors) |
| `show_detectors` | CSV detail level for `download_db` |

Both filters default `max_count` to `dashboard.max-streams` when zero.

---

## 2. Action-based repaging (§ `pagination.py`)

`action` is `<scope>_<op>` where scope ∈ {`mem`, `db`} and op ∈ {`first`, `last`,
`prev`, `next`, `refresh`}. It rewrites that scope's page offset; the other scope
is untouched. The C++ math (with `total` = that scope's available count and
`mc` = its `max_count`):

```
first   -> 0
last    -> max(0, total - mc)
prev    -> max(0, offset - mc)
next    -> max(0, min(total - mc, offset + mc))
refresh -> offset unchanged
```

`total` for the active scope is `getActiveStreamCount()`. For the finished scope
the C++ uses `getStreamCount() - inMemoryList.count()`. **Decision (port):** the
"− in-memory count" subtraction is a pre-existing quirk (active in-progress
streams are not in the DB, so subtracting them from the DB total is meaningless
and depends on call order). The Python port uses `total = getStreamCount()` for
the finished scope. See `pagination.resolve_page_offset`.

---

## 3. Active list pipeline (`InMemoryList::prepareList`)

Operates on a snapshot of the in-memory active-stream registry
(`Backend.mReportMap`, keyed by stream id), in this order:

1. **Sort** by `sort_field` (direction asc/desc). Supported fields:
   `sevana_mos`, `sevana_rfactor`, `src_ip`, `dst_ip`, `network_mos`, `jitter`,
   `duration` (= last.end − first.start), `sip_src`, `sip_dst`. Empty field →
   stream-id order; any other → `start_time`.
2. **Date window** (`mem_*_timestamp`): keep streams whose first-report start
   (seconds) is within `[start, end]`.
3. **Exact Call-ID** (`sip_callid`): keep only matching streams.
4. **Expression** (`mem_filter` expr): keep streams where the filter evaluates
   truthy, via the in-memory value-map (FILTER.md §4). Any eval error excludes
   the stream.
5. **Total count** = size after all filtering.
6. **Page slice** = `[offset, offset+max_count)`.

Implemented generically in `active_list.paginate_active` over value-map records,
reusing `vq_db.filter.evaluate_stream`.

---

## 4. Finished list pipeline (`DbList`)

Thin wrapper over `DatabaseStorage::makeStreamSearch(filter)`, which builds the
SQL (base join `rtpmon_statistics ⋈ rtpmon_streams ⋈ rtpmon_instances`, plus the
date-window, Call-ID, and `db_filter` WHERE fragment from `build_where`, plus
ORDER BY/LIMIT/OFFSET from `build_order_by`). Counts:
- With no expression / date / Call-ID filter → `available_finished_streams_count`
  = `getStreamCount()` (cheap total).
- Otherwise → a `count(*)` over the same WHERE (`FindTotalCount`).

This belongs to the DB layer (PLAN.md Phase 1/2) and is not implemented here yet;
the WHERE/ORDER BY builders it will call already exist in `vq_db.filter`.

---

## 5. Response envelope

`FillAgentInfo` (agent id/name) is always present. Then, depending on flags:

```jsonc
{
  "agent": { "id": "...", "name": "..." },
  "total_active_streams_count":      <int>,   // show_active
  "active_streams":                  [ <row>, ... ],
  "available_active_streams_count":  <int>,   // show_active or mem_show_count
  "total_finished_streams_count":    <int>,   // show_finished
  "finished_streams":                [ <row>, ... ],
  "available_finished_streams_count":<int>    // show_finished or db_show_count
}
```

Each `<row>` (from `StreamList::get`, identical shape for both lists):

| field | source / notes |
|-------|----------------|
| `stream_id` | first report's stream id string |
| `ref_id` | `link_id` string (used by `/streamhistory`, `/report`, `/audio`) |
| `instance` | `{id, name}` (falls back to config agent when empty) |
| `start_time` | first report start, formatted `ms→string` |
| `duration` | `(last.end − first.start)/1000` seconds (float) |
| `duration_audio` | sum of per-report audio time |
| `sevana_mos` / `network_mos` / `jitter` | last report (0 if uninitialized) |
| `sevana_rfactor` | last report |
| `source` / `destination` | stream id endpoints, first 5 chars stripped |
| `sip_src` / `sip_dst` | SIP peers (`%23`→`#`) |
| `sip_callid` | SIP Call-ID |

Rows with an empty report list are skipped. `mCount` (rows on page) is the
appended row count.

---

## 6. Implemented here

- `vq_db/stats/pagination.py` — `resolve_page_offset(action, offset, max_count,
  total)` and `parse_action(action) -> (scope, op)`. Pure, tested.
- `vq_db/stats/active_list.py` — `paginate_active(records, filter) ->
  (page, total)`: the §3 sort→date→callid→expression→slice pipeline over
  value-map records. Pure, tested.

Still to do (later phases): the active-stream **registry** (fed by the ZMQ bus),
the DB query layer behind `DbList`, the row serializer, CSV download, and the
HTTP handler that wires flags → these pieces.
