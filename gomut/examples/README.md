# gomut examples

Runnable demonstrations of mutation testing with [gomut]. They show exactly
what a mutation report looks like and why the score matters.

[gomut]: ../README.md

## Quick take

The two example modules below share the **exact same production source**
(`Between`, `Abs`, `Sum` in `weak.go`/`strong.go`) but differ only in test
strength:

| Module | Tests | Mutants | Detected | No coverage | Mutation score |
|---|---|---:|---:|---:|---:|
| [`01-surviving-mutants`](01-surviving-mutants) | weak | 17 | 5 | 2 | **33.3%** |
| [`02-strong-tests`](02-strong-tests) | strong | 17 | 14 | 0 | **82.4%** |

Same code, different test quality, very different scores. That gap is the whole
point of mutation testing.

## What mutation testing measures

`go test` tells you whether tests pass. Mutation testing tells you whether your
tests are *strong enough to catch a bug*: `gomut` injects a small, single-site
"mutant" into your production code (flip `&&` to `\|\|`, change `<` to `<=`,
turn `i++` into `i--`, bump a constant, ...) and runs the package's own tests
against it.

- **KILLED** — a test failed on the mutant. Your tests caught the bug.
- **SURVIVED** — every test still passed. Your tests are too weak to catch it.
- **TIMED_OUT** — the mutation looped forever; it was caught by a timeout.
- **NO_COVERAGE** — no test reaches this line, so the mutant could not be killed
  *or* survived.

A 100% `go test -cover` package can still have a 30% mutation score. Coverage
asks *"did the tests execute this line?"*; mutation asks *"would the tests have
noticed if this line were wrong?"*. The latter is a much stronger guarantee.

## How to run

From anywhere inside one of the example modules:

```console
$ cd gomut/examples/01-surviving-mutants
$ go build -o /tmp/gomut ../../cmd/gomut     # or: go install github.com/oliveagle/gomut/cmd/gomut
$ /tmp/gomut -v .
```

`-v` prints the per-mutant progress; the summary is written to stdout (and to
the `-report` directory as `report.txt` / `report.json`). See the flags in
[`../gomut/README.md`](../gomut/README.md) for the full list (`-threshold`,
`-mutators`, `-format`, `-no-cache`, ...).

## Example 1 — weak tests leave mutants alive

```text
Mutation testing: 33.3% mutation score (5/15 detected, 2 no coverage)
  (raw: 5/17 = 29.4%)

Per-operator:
  operator                total detected      score
  BooleanSwap                 1      0       0.0%
  ConditionalsBoundary        4      0       0.0%
  Constant                    3      1      33.3%
  Increments                  1      0       0.0%
  Math                        1      0     100.0%
  NegateConditionals          2      2     100.0%
  ReturnVals                  5      2      50.0%

github.com/oliveagle/gomut/examples/01-surviving-mutants (17 mutants)
  SURVIVED     BooleanSwap        weak.go:13  replaced "&&" with "||"
      | ok  	github.com/oliveagle/gomut/examples/01-surviving-mutants	0.002s
  KILLED       ReturnVals         weak.go:13  return value replaced with false (bool)  [killed by TestBetween]
      | --- FAIL: TestBetween (0.00s)
      |     weak_test.go:10: want true for an in-range value
      | FAIL
  ...
```

This package's tests **pass**, yet 10 of 17 mutants survive and 2 lines are
never covered. The tool names the survivors: `weak.go:13` flips `&&` to
`\|\|` (`Between(5,0,10)` returns `true` either way because nothing ever tests
an out-of-range value), and `weak.go:18` turns `x < 0` into `x <= 0` (the
negative branch is never exercised). Those are exactly the bugs `go test`
hides.

## Example 2 — strong tests kill almost everything

The identical source with thorough boundary/branch tests:

```text
Mutation testing: 82.4% mutation score (14/17 detected, 0 no coverage)
  (raw: 14/17 = 82.4%)

github.com/oliveagle/gomut/examples/02-strong-tests (17 mutants)
  KILLED       BooleanSwap        strong.go:13  replaced "&&" with "||"  [killed by TestBetween]
      | --- FAIL: TestBetween (0.00s)
      |     strong_test.go:18: Between(-1,0,10) = true, want false
      | FAIL
  KILLED       ConditionalsBoundary strong.go:13  replaced "v <= hi" with "v < hi"  [killed by TestBetween]
      | --- FAIL: TestBetween (0.00s)
      |     strong_test.go:18: Between(5,0,5) = false, want true
      | FAIL
  ...
  SURVIVED     Constant           strong.go:18  constant 0 replaced with 1   (equivalent)
      | ok  	github.com/oliveagle/gomut/examples/02-strong-tests	0.002s
  SURVIVED     ConditionalsBoundary strong.go:18  replaced "x < 0" with "x <= 0" (equivalent)
      | ok  	github.com/oliveagle/gomut/examples/02-strong-tests	0.002s
```

Three mutants still survive — but here they are **equivalent**: `constant 0→1`
in `x < 0` and `i := 0`, and `<`→`<=`, all of which behave identically to the
original for every input. A thorough test *cannot* kill an equivalent mutant,
and correctly reporting that (rather than treating every survivor as a real
weakness) is what keeps the score honest. The two `TIMED_OUT` entries are the
`i++`→`i--` and `i < n` negations in `Sum`, which loop forever and are caught by
the per-mutant timeout.

## Reading the report

### The mutation score

```
mutation score = detected / (total − no_coverage)
detected       = killed + timed_out
```

`no_coverage` is excluded from the denominator so untested code does not drag
the score down. In the examples above the *main* score is 33.3% and 82.4%
respectively. The *raw* percentage (`detected / total`, 29.4% / 82.4%) counts
every mutant; the headline score excludes `no_coverage`. A score of 100%
denominator (everything is `no_coverage`) is defined as 100% ("nothing to
detect"), mirroring pitest.

### Statuses

| Status | Meaning |
|---|---|
| `KILLED` | a test failed against the mutant — your tests caught the bug |
| `SURVIVED` | all tests still passed — your tests are too weak to catch it |
| `TIMED_OUT` | the mutation looped forever; caught by the timeout |
| `NO_COVERAGE` | no test executes this line — the mutant was never run |
| `COMPILE_ERROR` / `RUN_ERROR` | the mutation did not compile, or the runner failed |

The text renderer shows survivors and non-pass outcomes by default and prints,
for each killed mutant, the exact failure output that killed it (see
`[killed by TestBetween]` above). The JSON renderer reports the full mutant list
plus an aggregated `score` block:

```json
{
  "tool": "gomut",
  "version": "0.1.0",
  "generated": "2026-09-01T14:33:37.155460409+08:00",
  "packages": [ ... ],
  "score": {
    "total": 17, "detected": 14, "noCoverage": 0,
    "killed": 12, "survived": 3, "timedOut": 2,
    "compileError": 0, "runError": 0,
    "main": 82.35294117647058, "raw": 82.35294117647058
  },
  "byOperator": [ ... ]
}
```

### Why a mutation report is useful

- **Gate CI on a mutation-score threshold** (`-threshold=N` exits non-zero when
  the score falls below `N`), so a test suite cannot regress in strength
  unnoticed.
- **Find weak tests cheaply.** A survivor usually means a missing case
  (boundary, branch, out-of-range value), far cheaper to chase down than a
  coverage gap alone can tell you.
- **Prove test strength.** Coverage counts lines executed; the mutation score
  counts how well those lines defend against change. Run them together to see
  both *"did we cover it?"* and *"would we have caught a bug there?"*.

## Files

| Path | What it is |
|---|---|
| `01-surviving-mutants/` | module with deliberately weak tests |
| `02-strong-tests/` | module with strong tests over the same source |
| `weak.go` / `strong.go` | shared production source (`Between`, `Abs`, `Sum`) |
| `README.md` | this walkthrough |
| `../../internal/engine/alltests_test.go::TestExamplesEffectiveness` | living regression that runs both modules and asserts the contrast (skips with `-short`) |

Re-run the demonstration yourself:

```console
$ cd gomut/examples/01-surviving-mutants && /tmp/gomut -report . -v .
$ cd ../02-strong-tests && /tmp/gomut -threshold 90 -v .   # exits 2: score 82.4% < 90%
```
