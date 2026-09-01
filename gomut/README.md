# gomut — mutation testing for Go

`gomut` runs **mutation testing** on Go packages. It injects small, single-site
"mutations" into production code and measures whether the package's own test
suite catches each one. A mutation that a test fails against is **killed**;
one that no test notices is **survived**, which means the tests are too weak to
have caught a real bug there.

It depends only on the Go standard library and the local toolchain.

## What mutation testing measures

`go test` tells you whether tests pass. Mutation testing tells you whether your
tests are *strong enough to catch a bug*. Given a deliberately-broken version of
your code (a mutant), mutation testing asks: *does my test suite fail when this
mutation is present?*

Each mutant ends up in exactly one state:

| status | meaning |
|---|---|
| `KILLED` | a test failed against the mutant — your tests caught it |
| `SURVIVED` | every test still passed — your tests are too weak to catch this |
| `TIMED_OUT` | the mutation made a test loop forever; it was killed by a timeout |
| `NO_COVERAGE` | no test reaches this code, so the mutation could neither be killed nor survive |

`gomut` reports the **mutation score**:

```
mutation score = detected / (total − no_coverage)
detected       = killed + timed_out
```

A mutant in `NO_COVERAGE` is excluded from the denominator, so code your tests
never exercise does not drag the score down. See
[ADR-0001](../docs/adr/0001-gomut-go-mutation-testing.md)
for the exact definitions.

## Relationship to gobco

`gomut` lives in this repository right next to [`gobco`](../README.md):

| | gobco | gomut |
|---|---|---|
| **measures** | branch/condition coverage | mutation score |
| **asks** | "did the tests run over this code?" | "would the tests notice if this code were wrong?" |
| **relies on** | coverage data from `go test -coverprofile` | injecting mutants and re-running tests |

They are complementary. A package can have 100% coverage and still 0% mutation
score — your tests exercise the code but are too weak to catch a bug in it. Run
both to get a fuller picture of test quality.

## Installation

`gomut` builds in place from the source tree:

~~~text
$ go build -o gomut ./cmd/gomut
$ ./gomut -version
gomut 0.1.0
~~~

It also installs cleanly into `$GOPATH/bin`:

~~~text
$ go install github.com/oliveagle/gobco/gomut/cmd/gomut@latest
~~~

## Usage

~~~text
$ gomut [flags] [packages...]
~~~

Run it from inside the module that contains the code you want to mutate. A
package pattern follows the usual `go build` conventions (`.`, `./...`).

~~~text
# mutate every package in the current module
$ gomut ./...

# only the Math and Constant operators, gate CI on an 80% score
$ gomut -mutators=Math,Constant -threshold=80 .

# run the whole test suite per mutant (instead of the coverage-selected subset)
$ gomut -all-tests -v ./pkg
~~~

### Flags

| flag | default | description |
|---|---|---|
| `-p int` | CPU count (capped at 8) | number of parallel test subprocesses |
| `-timeout duration` | `30s` | per-mutant test run budget; longer runs end in `TIMED_OUT` |
| `-mutators string` | all built-ins | operator set: `default`, `all`, `none`, or a comma list of names; a `-`-prefixed name removes an operator from the default set (`-Math,-Constant`) |
| `-all-tests` | off | run the whole test suite per mutant instead of the coverage-selected subset |
| `-threshold int` | `0` | exit with code 2 if the mutation score (percent) is below this value |
| `-report string` | `.gomut-report` | report output directory; empty disables file output |
| `-format string` | `text` | comma-separated report formats to write: `text`, `json` |
| `-no-cache` | off | disable the per-package result cache |
| `-cover-test` | off | also mutate `_test.go` files *(not implemented in v1)* |
| `-v` | off | print per-mutant progress |
| `-version` | | print the version and exit |
| `-help` | | print usage and exit |

## Built-in operators

`gomut` ships eight mutation operators (the default set). Type-aware operators
(`Math`, `Constant`, `ReturnVals`) consult type information and degrade to
syntactic-only behaviour — or skip — when it is unavailable, so no mutant is
ever a "junk" mutation that does not correspond to a real code change.

| operator | what it does |
|---|---|
| `ConditionalsBoundary` | shift relational operators by one (`<`↔`<=`, `>`↔`>=`, `==`↔`!=`) |
| `NegateConditionals` | negate a boolean condition (`a < b` → `a >= b`) |
| `InvertNegs` | flip the sign of a numeric constant (`1` → `-1`) |
| `BooleanSwap` | swap boolean literals (`true` ↔ `false`) |
| `Math` | replace a binary numeric operator (`*`↔`/`, `+`↔`-`, …) |
| `Increments` | flip `++`/`--` and increment/decrement targets |
| `Constant` | replace a numeric constant with another value |
| `ReturnVals` | replace a return value with a constant |

## Reading the output

`gomut` prints progress to `stderr` and the report to `stdout`. The summary
below comes from running it on the fixture in `testdata/sample`:

~~~text
$ gomut -p 2 ./...
gomut: 18 mutants in 6.9s

Mutation testing: 87.5% mutation score (14/16 detected, 2 no coverage)
  (raw: 14/18 = 77.8%)

Per-operator:
  operator                total detected      score
  ConditionalsBoundary        3      2      66.7%
  Constant                    5      3      75.0%
  Increments                  2      2     100.0%
  Math                        1      0     100.0%
  NegateConditionals          2      2     100.0%
  ReturnVals                  5      5     100.0%

github.com/oliveagle/gobco/gomut/testdata/sample (18 mutants)
  KILLED       ConditionalsBoundary math.go:11  replaced "v > 0" with "v >= 0"
  KILLED       ReturnVals         math.go:11  return value replaced with false (bool)
  SURVIVED     ConditionalsBoundary math.go:18  replaced "a < 0" with "a <= 0"
  KILLED       NegateConditionals   math.go:18  negated condition a < 0
  TIMED_OUT    Increments         math.go:29  replaced i++ with i--
  NO_COVERAGE  Constant           math.go:38  constant 2 replaced with 3
  NO_COVERAGE  Math               math.go:38  replaced "x * 2" with "x / 2"
~~~

The raw percentage (`14/18 = 77.8%`) counts every mutant in the denominator;
the headline score (`87.5%`) excludes the two `NO_COVERAGE` mutants, matching
the definition above. The `-v` flag additionally prints, for each killed mutant,
*which* test killed it and the failure output it produced.

### Exit codes

| code | meaning |
|---|---|
| `0` | run succeeded (and score is above any `-threshold`) |
| `1` | usage or environment error (bad flags, no operators selected, engine failure) |
| `2` | the mutation score is below the `-threshold` |

The engine and operator code is intentionally kept dependency-free and readable;
the full design — overlay-based mutation, per-mutant subprocess execution, the
result cache, and the operator set — is documented in the [architecture decision record](../docs/adr/0001-gomut-go-mutation-testing.md).

## How it works

For each package `gomut`:

1. **baseline** — runs the package's tests with `go test -coverprofile` to learn
   which tests reach which lines.
2. **select** — picks the mutants whose source line is covered by the baseline,
   using the per-test cover profiles.
3. **mutate + execute** — for each mutant, applies the single-site change via
   `go test -overlay`, then runs the selected tests in an isolated subprocess
   (`setpgid`, killed on timeout, up to `-p` at a time) to classify it.
4. **report** — aggregates the per-mutant results into the mutation score and
   the per-operator/per-mutant breakdown above.

The per-package cache keys on the source, test, operator set, and Go version, so
unchanged input is skipped unless `-no-cache` is given.

## Caveats (v1)

`gomut` v1 is a focused first cut:

- `-cover-test` (mutating `_test.go` files) is accepted but not implemented; the
  CLI prints an error and exits `1`.
- HTML reports are a planned v2 feature; `-format=html` is rejected with an error.
- Only `go.mod` modules are handled; only production code is mutated (no HTML,
  no cross-module analysis).

See the [open todos](../docs/todo.md) for the tracked v2 work.
