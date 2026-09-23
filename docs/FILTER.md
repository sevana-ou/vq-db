# vq-db dashboard filter mini-language — deep dive

Companion to `PLAN.md` §3.2. This pins down the exact behavior of the `/stats`
filter expression so it can be re-implemented in Python with known fidelity.

Source of truth (all C++):
- Grammar / lexer / parser / AST / in-memory eval:
  `platform/rtphone/src/engine/helper/HL_Calculator.h` (header-only; `.cpp` empty).
- Value semantics (`Variant`): `platform/rtphone/src/engine/helper/HL_VariantMap.cpp`.
- Thin wrapper: `source/vq_db/dashboard/dashboard_filter.{h,cpp}`.
- SQL name-map + query assembly: `source/vq_db/database.cpp` ~L1640–1792.
- In-memory value-map: `source/vq_db/dashboard/dashboard_data.cpp` ~L495–537.
- Filter string transport: `source/vq_db/search_filter.cpp`.

The expression reaches us as `SearchFilter.mExpression` after the transport layer
decodes `field/dir/offset/count/<hexExpr>` (hex; one path also Base64-encodes it).

---

## 1. Two evaluation targets, one AST

The same parsed AST is used in two places and they are **not guaranteed to agree**:

- **DB list** → `DashboardFilter::getAsSql(nameMap)` → `Item::print()` emits a SQL
  `WHERE` fragment, AND-appended to the `rtpmon_statistics ⋈ rtpmon_streams ⋈
  rtpmon_instances` query.
- **Active (in-memory) list** → `DashboardFilter::eval(valueMap)` → `Item::eval()`
  walks the AST against a `Variant` map built from the live `QualityReport`.

A stream is **kept** when the expression evaluates truthy. In memory the code does
`std::remove_if(..., return !f.eval(vm).asBool())`. On any exception
`DashboardFilter::eval` returns `Variant(false)` ⇒ the stream is **excluded**.
In SQL, a parse/throw sets `whereClause = "false"` ⇒ **no rows**.

---

## 2. Lexer (`getLexem`)

Character classes and tokens:

| Input | Token | Notes |
|-------|-------|-------|
| `(` `)` | OpenBracket / CloseBracket | returned immediately |
| `0`–`9` | Dec | consumes digits and `.`; `0x` prefix switches to Hex |
| `0x…` | Hex | hex digits after `0x` |
| contains `.` | Float (if parses as float) | else if it parses as an IP → **String**; else stays Dec |
| `a`–`z`/`A`–`Z` | Var | then consumes `[A-Za-z0-9._]` |
| `"…"` | Str | quotes included then stripped in `makeAst` |
| `+ - * /` | Oper (1-char) | finished immediately |
| `< <= > >= == \|\| &&` | Oper (2-char where applicable) | see below |

Operator subtleties (this is where surprises live):
- `+ - * /` are **always single-char** (finished in `processNewLexem`).
- `<` and `>` are valid **single-char** operators; they extend to `<=` / `>=`
  only if the next char is `=` (the code also lets a 2nd char of `< > = & |`
  attach, but `makeAst` only recognizes `<= >=`).
- `=`, `&`, `|` are **not** valid alone. Only `==`, `&&`, `||` are recognized by
  `makeAst`. A lone `=`/`&`/`|` yields an AST node with `Type::None` → later
  `eval`/`print` throws.
- **`!=` is unreachable.** `!` is not in the operator char set, so the lexer can
  never emit it, even though `makeAst`/`print`/`eval` handle `NotEqual`. Treat
  `!=` as **unsupported** for parity.
- Boolean keywords are the C-style `&&` / `||`. The words `and`/`or` are only
  produced on the **output** (SQL) side by `print()`.
- A `.`-containing numeric that is actually an IPv4/IPv6 literal becomes a String
  token (so `src_ip == 10.0.0.1` works without quotes).

Literal → `Variant` type in `makeAst`:
- Dec → `int64` (VTYPE_INT64), Hex → int, Float → `float` (VTYPE_FLOAT),
  Str → string.

---

## 3. Operators & precedence

`getOperatorLevel()` (higher binds tighter):

| Level | Operators |
|------:|-----------|
| 3 | `*` `/` |
| 2 | `+` `-` |
| 1 | `<` `<=` `>` `>=` |
| 0 | `==` (`!=` unreachable) |
| −1 | `&&` (And) |
| −2 | `\|\|` (Or) |

Parsing (`parseExpression`) is a hand-rolled recursive routine that builds a
right-leaning tree and then **rotates** when the right subtree is an operator of
**≤** precedence and not bracketed, to recover left-to-right evaluation order.
Brackets recurse and set `mHasBrackets`, which blocks rotation. This is delicate;
**do not port it literally** — reproduce the operator set + precedence table with
a standard precedence-climbing (Pratt) parser, left-associative, and validate
against the corpus in §7. Unary minus is **not** supported (no unary handling).

There is also a stray `std::cout << "Returned lexem: …"` on every token — debug
noise, irrelevant to the port.

---

## 4. `Variant` value semantics (in-memory path) ⚠️

Arithmetic and comparison **switch on the left operand's type** and then call a
typed accessor on the right operand. The accessors are strict:
- `asInt()` throws unless type is exactly `VTYPE_INT`.
- `asInt64()` throws unless exactly `VTYPE_INT64`.
- `asFloat()` accepts `INT`, `INT64`, `FLOAT` (real numeric coercion) — but **not**
  string.
- Comparisons: `>` is `!(==) && !(<)`; `<=`/`>=` derived; `==` on strings compares
  `asStdString()`.
- `asBool()`: numbers ≠ 0, non-empty string = true.

Consequences to be aware of (and to pin with tests):
- `float <op> int-literal` works (LHS float ⇒ `right.asFloat()` coerces). So
  `sevana_mos < 3` is fine (sevana_mos is float, `3` is int64).
- `int-var == int64-literal` **throws**: LHS int ⇒ `right.asInt()`, but the literal
  is `int64` ⇒ throws ⇒ stream excluded. E.g. an integer column like
  `sevana_rfactor == 90` can mis-behave depending on the variable's stored type.
- String-vs-number comparisons throw (excluded in memory; in SQL they don't).

These strict-throw rules are the main reason the in-memory and SQL paths diverge.

### In-memory value map (`dashboard_data.cpp`)
Keys and their source/type/units:

| key | value | type | unit |
|-----|-------|------|------|
| `start_time` | first report start | int64 | **seconds** (ms/1000) |
| `sevana_mos` | last report | float | |
| `network_mos` | last report | float | |
| `sevana_rfactor` | last report | int | |
| `jitter` | last report | float | ms |
| `rtt_delay` | last report | float | |
| `duration` | end − first start | int | **ms** |
| `src_ip`/`dst_ip` | stream id | string | |
| `src_port`/`dst_port` | stream id | int | |
| `ssrc` | stream id | string | decimal text |
| `sip_src`/`sip_dst` | SIP peers | string | |
| `sip_callid` | SIP call id | string | |
| `instance_id`/`instance_name` | agent | string | |

(Uses the **last** report for metrics but the **first** report for start_time.)

---

## 5. SQL generation (DB path)

`Item::print()` emits, per node, `( <left> <op> <right> )` where:
- Var → `nameMap[name]` (raw `name` if unmapped — injection / invalid-column risk).
- String → **double-quoted** `"v"` (SQLite treats as identifier-ish; breaks on
  PG/MySQL). **Port must emit single-quoted, escaped, ideally parameterized.**
- `==` printed as `==` (SQLite-only; **port should emit `=`**).
- `&&`/`||` printed as `and`/`or` (portable).
- Numbers printed bare.

### SQL name-map (`database.cpp` L1717–1736)
```
start_time      -> rtpmon_statistics.start_timestamp   (ms)   [in-mem uses seconds!]
sevana_mos      -> rtpmon_statistics.sevana_mos
sevana_rfactor  -> rtpmon_statistics.sevana_rfactor
network_mos     -> rtpmon_statistics.sevana_rfactor    ⚠ BUG: maps to rfactor, not network_mos
src_ip          -> rtpmon_streams.src_ip
src_port        -> rtpmon_streams.src_port
dst_ip          -> rtpmon_streams.dst_ip
dst_port        -> rtpmon_streams.dst_port
sip_source/sip_src       -> rtpmon_streams.sip_source
sip_destination/sip_dst  -> rtpmon_streams.sip_destination
sip_callid      -> rtpmon_streams.sip_callid
ssrc            -> rtpmon_streams.ssrc
rtt_delay       -> rtpmon_statistics.rtt_delay
duration_audio  -> rtpmon_statistics.duration_audio
duration        -> rtpmon_statistics.duration_audio    [in-mem uses end−start ms!]
instance_id     -> rtpmon_instances.agent_id
instance_name   -> rtpmon_instances.agent_name
```
Note SQL has **no** `jitter` mapping (filtering by jitter only affects the
in-memory list) and `network_mos`/`duration`/`start_time` have semantics that
differ from the in-memory map. These are pre-existing inconsistencies, not things
the Python port introduced — see §6 for the decision needed.

---

## 6. Divergences & bugs — DECISION: fixed in the Python port

The C++ has three behavior bugs and several memory-vs-SQL mismatches. The Python
port does **not** keep bug-for-bug compatibility; all of these are corrected (see
`stream_filter.py` / `expression.py`):

1. `network_mos` → now its own column (was `sevana_rfactor`). **Fixed.**
2. `start_time` → SQL divides to seconds, matching the in-memory unit. **Fixed.**
3. `duration` → SQL computes `end − start`, matching in-memory (was
   `duration_audio`). **Fixed.**
4. `jitter` → now has a SQL mapping (was in-memory only). **Fixed.**
5. Strict `Variant` int/int64 throw semantics (§4) → replaced by uniform numeric
   coercion, so SQL and in-memory agree. **Fixed.**
6. `!=` → supported by the new lexer/parser (was unreachable). **Added.**

This means the Python `/stats` results can differ from the legacy C++ for filters
that hit one of these bugs. That is intentional and approved.

---

## 7. Python implementation plan

**Module** `vq_db/filter/expression.py`:
- Tokenizer matching §2 exactly (incl. IP-as-string, `0x`, `[A-Za-z0-9._]` idents,
  the `< > <= >= == && ||` set; reject/ignore lone `= & |` and `!=` per parity, or
  enable `!=` if §6.6 fix is taken).
- Pratt parser with the §3 precedence table, left-associative, brackets, no unary.
- AST with two emitters:
  - `to_sql(name_map) -> (sql_text, params)` — **parameterized**; single-quoted
    strings; `=`/`<>`; whitelist mapped identifiers, reject unmapped names.
  - `evaluate(value_map) -> bool` — numeric-coercing comparisons (or strict to
    mirror `Variant` if parity mode).
- Sort-column + name maps centralized so SQL and memory stay in lockstep.

**Module** `vq_db/filter/search_filter.py`: parse/serialize
`field/dir/offset/count/<hexExpr>` exactly (hex decode; handle the Base64 variant),
sort-column whitelist, paging.

**Safety win**: the C++ string-interpolates user input into SQL (the filter values
and any unmapped identifier). The Python port should parameterize all values and
strictly whitelist identifiers — closing an injection hole while staying
output-equivalent for valid filters.

### Test corpus (build before coding `/stats`)
Capture real `mem_filter` / `db_filter` strings the Flutter app sends, plus
hand-written edge cases, and snapshot for each: the generated SQL (params) and the
in-memory boolean over a fixed set of stream fixtures, comparing Python vs the
running C++ vq-db. Seed cases:
```
sevana_mos < 3
sevana_mos < 3.6 && network_mos > 3
jitter > 20 || sevana_rfactor < 60
src_ip == 10.0.0.1
sip_src == "sip:alice@host" && sevana_mos <= 4
(sevana_mos < 3 || jitter > 30) && dst_port == 5060
ssrc == 0x1a2b
duration > 10
sevana_rfactor == 90          # int/int64 throw hazard (§4)
network_mos > 3               # column-mapping bug (§6.1)
```
For each, record C++ behavior first; that is the parity oracle.

---

## 8. Effort estimate
- Tokenizer + Pratt parser + two emitters: ~250–350 LOC.
- SearchFilter transport: ~60 LOC.
- Parity test harness + corpus: ~150 LOC + fixtures.
- Risk: **medium** — the language is tiny, but the coercion quirks and the
  memory/SQL divergences need a captured oracle to lock down. No third-party
  parser dependency is required.
