# Implementation Plan: dead-code-suite

## Overview

Four repositories, none of which exists yet. `deadset-spec` holds THE Contract and THE Conformance_Corpus and is the dependency of everything else. `deadset-go` and `deadset-ts` are independent analyzers that share no code and agree only because one corpus expectation fails both of them when they diverge. `deadset` resolves the cross-language edges neither analyzer can resolve alone, which is the one thing a workflow step concatenating two reports cannot do (settled decision 28); detecting the languages, running each analyzer as a process and returning one exit code follow from that.

The catalogue this plan builds is the 31 issue kinds settled decisions 27 and 32 admit: `DS1302` and `DS1303` promoted to enabled at `certain`, the six intra-function kinds enabled by default, and seven kinds narrowed by a precondition the analyzer checks before it reports (closed-world narrowing for `DS1101`, `DS1102` and `DS1104`; sum-type and marker exemptions for `DS1203`; function and method type parameters only for `DS1303`; a declared complete matrix for `DS1501`; `go mod tidy -diff` semantics for `DS1601`; an absent `replace` target for `DS1605`; `unparam`'s exemptions for `DS1801` and `DS1803`). The 19 kinds the two decisions removed have no task here and are listed in the requirements' Non-goals and the steering document's tiers with the tool that owns each or the mechanism that would admit it. Every document a product reads or writes is JSON decoded by the standard library (settled decision 29), so no task builds a parser. The orchestrator stays in the first release and its first task proves one real cross-language edge end to end before any provider-list, acquisition, container or Action work begins (settled decision 33).

Inside each analyzer the plan follows the design's own order: load, symbol inventory, references, graph, roots, mark and sweep, components, exemptions, classification, issue kinds, suppression, reporters. Nothing in this fleet's existing gate is touched by any task here; the shared-workflow edits and the per-repository adjudication passes are named under Follow-up work.

## Critical path

1. `deadset-spec`, tasks 2 to 8. The code space, the finding schema, the grammars, the configuration schema, the merge specification and the corpus format are read by every later task in every repository.
2. `deadset-go` (tasks 10 to 27) and `deadset-ts` (tasks 29 to 41) proceed in parallel from there. Neither depends on the other at any point, by Requirements 1.8 and 1.9.
3. `deadset` (tasks 43 to 50) proceeds in parallel with both for the merge, which is built against the published vectors of task 6 rather than against a live analyzer. That is what the vectors are for. Task 43.1, the edge proven first, needs both analyzers able to emit an edge evaluation (tasks 23.5 and 37.4) and the merge of tasks 45 and 46, and it gates the provider list (43.4), the handshake (44.1), acquisition (48) and packaging (49) by settled decision 33. Tasks 43.1, 43.5, 47.4 and 53 need a finished analyzer.
4. Cross-product tasks 52 to 54 need both analyzers emitting conforming reports.
5. Tasks 55 to 57 are the release gates. No analyzer ships as a gate before its measurement run is recorded and a maintainer's review of it is recorded (Requirements 34.22, 36.8).

## How to read a task

- A parent task groups one subsystem. The leaf sub-tasks are the unit of work, each finishable and verifiable on its own.
- Every leaf states its verification first, then the requirements it implements and the design section that says how. Verification is always mechanical: a test, a fixture, a corpus run, a golden file, a published vector, a measured number, or a command with an expected exit code.
- Every detection leaf carries the red-check discipline: one fixture in which the kind or the exemption fires, one in which it must not, and a recorded demonstration that the assertion fails with the implementation reverted (Requirements 34.1, 34.2, 34.13).
- Where a leaf adds an issue kind or an exemption class, writing that kind's corpus fixture and its language-neutral expectation is part of the leaf rather than a later task (Requirement 2.15).
- The design's 33 correctness properties are implementation work. Each one is a leaf under the parent that owns the code it constrains, at minimum 100 iterations, tagged `dead-code-suite/P<n>`. Each property is implemented once; where the same behavior exists in the other product, the corpus expectation and the published vectors bind it (Requirement 2.19, Property 33).

## Tasks

### `deadset-spec`: the Contract and the corpus

- [x] 1. Create the four repositories
  - [x] 1.1 Bootstrap `cplieger/deadset-spec` through this fleet's repo-bootstrap process, as a data-only repository with no built artifact.
    - Verify: the bootstrap process's own audit reports no drift, and the first release exists. _Requirements: 1.5, 1.6, 1.7, 32.13. Design: Architecture._
  - [x] 1.2 Bootstrap `cplieger/deadset-go` through the same process, as a Go module publishing `cmd/deadset-go`.
    - Verify: the audit reports no drift, `go build ./...` succeeds on the skeleton, and `go version -m` on the built skeleton binary lists nothing outside the standard library, `golang.org/x/tools` and the modules `x/tools` itself links. _Requirements: 1.3, 1.7, 28.4, 32.9, 32.13. Design: Architecture._
  - [x] 1.3 Bootstrap `cplieger/deadset-ts` through the same process, as a TypeScript package published to npm and to JSR from one tag.
    - Verify: the audit reports no drift, and the manifest pins one exact `typescript` version at 7.0.2. _Requirements: 1.4, 1.7, 26.9, 32.10, 32.13. Design: Architecture._
  - [x] 1.4 Bootstrap `cplieger/deadset` through the same process, as a Go module publishing `cmd/deadset`.
    - Verify: the audit reports no drift, and `go build ./...` succeeds on the skeleton. _Requirements: 1.1, 1.7, 32.8, 32.13. Design: Architecture._

- [ ] 2. The code space and the vocabularies
  - [x] 2.1 Write `contract/contract.json` and `contract/exit-codes.json`: the Contract version, the supported platform set, the accepted schema versions, and the five exit codes with their meanings.
    - Verify: a spec self-test asserts every exit code in the table is one of 0 to 4 and that each carries a distinct meaning. _Requirements: 2.20, 25.1 to 25.9, 32.1. Design: The Contract, The exit-code table._
  - [x] 2.2 Write `contract/kinds.json`: the eight code ranges and all 31 kind rows, each carrying the code, name, language, default enablement, severity, maximum reachability class, fixability, the precondition where settled decisions 31 and 32 narrowed the kind, and the per-language overlapping linter for the six intra-function rows, plus one retired row per code settled decisions 27 and 32 removed, nineteen in all, naming the decision.
    - Verify: a spec self-test asserts each code is unique, sits in exactly one range, carries every field, and that `DS1703` carries `"fixed": true`; a second assertion fails if any `DS18xx` row omits an overlap value for either language, `none known` included; a third asserts every retired code, `DS1607` and `DS1608` included, is absent from the live rows and that no live row declares a maximum class below `certain`. _Requirements: 2.1 to 2.7, 2.20, 13.5, 20.1, 20.4, 23.5. Design: The code space, Issue kinds, The intra-function group._
  - [x] 2.3 Write `contract/exemptions.json`: every exemption class with its documented detection rule and the language it applies to, including the `private` versus `#private` distinction the reflective classes turn on.
    - Verify: a spec self-test asserts every class named in any corpus expectation resolves to a row here. _Requirements: 14.1, 14.3 to 14.10. Design: Exemptions (Go), Exemptions (TypeScript)._
  - [~] 2.4 Write the vocabulary self-test suite covering the closed-vocabulary rules of 2.1 to 2.3 plus retirement: a code assigned to two kinds fails, a code removed from `kinds.json` may not be re-assigned, and a live row may not carry a code from the retired `DS1400` range or from the nineteen retired codes.
    - Verify: the suite fails on a planted duplicate code, a planted range straddle, a planted re-assignment of `DS1402`, a planted re-assignment of `DS1607` and a planted live row at `DS1450`. _Requirements: 2.3, 2.4. Design: The code space._

- [ ] 3. The finding schema and the report envelope
  - [~] 3.1 Write `contract/finding.schema.json` as JSON Schema 2020-12, including the `details` object discriminated on `code` with one branch per kind that carries extra data.
    - Verify: the design's example finding validates; one negative document per required field fails; a `details` branch whose discriminator does not match its shape fails. _Requirements: 2.8, 2.9, 13.1, 14.2, 23.4, 24.2. Design: The report envelope and the finding schema._
  - [~] 3.2 Write `contract/report.schema.json`: the envelope with `analyzer`, `target`, `configurations`, `consumers`, `findings`, `edge_evaluations`, `stale_suppressions`, `declared_gaps`, `excluded_by_cgo`, `test_file_rules` and `totals`, an edge evaluation being `{edge, side, symbol, state, finding?}` with the finding present exactly when the state is `dead`.
    - Verify: the design's example envelope validates; an evaluation with state `dead` and no finding fails, and one with state `live` and a finding fails; a finding carrying an edge field of its own fails; an envelope with no `analyzer.conformance` block fails. _Requirements: 2.8, 2.9, 21.13, 24.2, 28.10, 30.14, 31.4, 31.11. Design: The report envelope and the finding schema, The cross-language edge._
  - [~] 3.3 Write the schema validation suite over a committed example set, one example per issue-kind family and one per envelope state, running in `deadset-spec`'s own continuous integration with whatever JSON Schema tool a data-only repository chooses; no product runs a JSON Schema validator at run time.
    - Verify: the suite validates every example and fails on every committed negative, and every fixture report a product commits is validated here rather than in the product. _Requirements: 2.8, 2.9. Design: The report envelope and the finding schema, Invocation, and the handshake before it._

- [ ] 4. The grammars
  - [x] 4.1 Write `contract/grammar/suppression.md`: the inline-directive grammar with both comment spellings, the ignore-entry and baseline-row JSON shapes with `reason` required on both, the `deadset:` namespace, and the rule that a matched suppression marks its symbol live before the sweep, plus a token corpus of accepted and refused strings.
    - Verify: the token corpus carries a refused case per rule, being a missing reason, a bare symbol with no path, a foreign namespace and a directive on the wrong line. _Requirements: 2.10, 21.1 to 21.8, 21.15. Design: Suppression._
  - [x] 4.2 Write `contract/grammar/symbol-ref.md`: the stable symbol-reference grammar with one section per language, plus a token corpus binding each form to a rendered example.
    - Verify: the corpus carries a Go package-level, method and field form and a TypeScript module, class-member and enum-member form, each with its refused near-miss. _Requirements: 2.5, 31.1, 31.2. Design: The stable symbol reference._
  - [x] 4.3 Write `contract/grammar/text-line.md` and `contract/grammar/sarif.md`: the position-first line format with the regular expression both products' golden tests share, and the SARIF 2.1.0 mapping including the omitted-suppression rule and the two fingerprint keys.
    - Verify: the SARIF section names, for every property it emits, the GitHub supported-properties row that reads it, and states that `suppressions` is not emitted. _Requirements: 2.5, 2.12, 24.1, 24.4. Design: The text line format, The SARIF 2.1.0 mapping._
  - [~] 4.4 Write the grammar corpus self-test: every accepted string parses under the documented grammar and every refused string does not.
    - Verify: the suite fails when one refused string is moved into the accepted set. _Requirements: 2.10, 2.12. Design: Suppression, The text line format._

- [ ] 5. The configuration schema and the configuration vectors
  - [x] 5.1 Write `contract/config.schema.json` as a closed key list, each key carrying its type, its default, the product that owns it and whether it is required, including `consumers.complete`, `matrix.complete` and the `provenance` key that is accepted on input and ignored by resolution.
    - Verify: a spec self-test asserts no key admits arbitrary nesting, `target.kind` carries no default, and `provenance` is marked ignored. _Requirements: 12.13, 15.8, 27.1 to 27.3, 27.7, 27.9, 27.10, 27.15, 27.18. Design: Configuration, The configuration format, and why nothing parses it._
  - [~] 5.2 Write `vectors/config/`: the seven cases the design names, each with its input documents, its expected resolved configuration and its expected exit code.
    - Verify: the case set covers a `provenance` object on input, a quoted key with a dot, an array spanning lines, an unimplemented key, a missing target kind, a duplicated key and the round trip of a resolved-configuration printout. _Requirements: 27.4 to 27.6, 27.8, 27.11 to 27.15. Design: The configuration format, and why nothing parses it._
  - [~] 5.3 Write the configuration vector self-test: every expected resolved configuration names only keys the schema declares, and every expected exit code sits in the exit-code table.
    - Verify: the suite fails on a planted vector naming an undeclared key. _Requirements: 27.15, 25.3. Design: The configuration format, and why nothing parses it._

- [ ] 6. The merge specification and the merge vectors
  - [x] 6.1 Write `contract/grammar/merge.md`: the seven steps, the admission rules, the canonical key with its stated tiebreak order, and the statement that language is deliberately absent from the key.
    - Verify: the document states, per step, the requirement it answers and the exit code it can produce. _Requirements: 2.13, 30.16, 30.17, 31.6 to 31.10. Design: The merge._
  - [~] 6.2 Write `vectors/merge/`: the ten cases the design names, each with its input reports, its accepted schema range, its byte-exact expected merged report and its expected exit code.
    - Verify: the case set covers one report only, two reports with no edges, a pending pair that is live, a pending pair that is dead, an edge absent from every other report, an edge every side reports absent, two analyzers claiming one language, a schema version outside the range, an analyzer with no conformance pass, and a report holding a stale suppression. _Requirements: 2.14, 30.15, 30.17, 31.6 to 31.10. Design: Merge test vectors._
  - [~] 6.3 Write the merge vector self-test: every input and every expected report validates against `report.schema.json`, every expected merged report holds no edge evaluation carrying a finding, and the finding order matches the canonical key.
    - Verify: the suite fails on a planted expected report holding an evaluation with a finding, and on one whose findings are out of canonical order. _Requirements: 2.13, 2.14, 31.10. Design: The merge, Merge test vectors._

- [ ] 7. The Conformance Corpus format and its runner contract
  - [x] 7.1 Write `corpus/corpus.json`, the `expect.json` schema and the per-rendering `fixture.json` manifest schema, with the fixture-local logical symbol name as the only binding between an expectation and a language rendering.
    - Verify: a spec self-test asserts no expectation field names a language, and that every logical name in an expectation resolves through each rendering's manifest to a file and a line. _Requirements: 2.15, 34.21. Design: The Conformance Corpus._
  - [x] 7.2 Write the `conformance.json` declared-gap schema and the `conformance-results.json` schema, with the per-fixture result value set of pass, gap and fail.
    - Verify: a gap row missing its reason fails; a results document naming a fixture absent from the corpus fails. _Requirements: 2.17, 2.18, 30.14, 34.15. Design: Declared gaps._
  - [~] 7.3 Write the three seed fixtures the format's own proof needs: `unused-exported-consumer` in both languages, `interface-satisfaction-conversion` in Go, `private-member-unread` in TypeScript.
    - Verify: each fixture's expectation names only closed-vocabulary values, and the two-language fixture carries one expectation file binding both renderings. _Requirements: 2.15, 2.19, 34.21. Design: The Conformance Corpus._
  - [~] 7.4 Write the corpus self-test: closed vocabularies, a missing rendering for a declared language, and an expectation a product omits with no gap recorded.
    - Verify: the suite fails on a planted expectation naming a code outside `kinds.json`, on a planted class outside `exemptions.json`, and on a silently omitted expectation. _Requirements: 2.16 to 2.18, 34.14, 34.15. Design: The Conformance Corpus, Declared gaps._
  - [~] 7.5 Write the cross-language agreement checker: it reads two `conformance-results.json` files and asserts, per shared expectation, that the code, the confidence and the suppression behavior agree.
    - Verify: run against synthetic result files; a planted divergence fails naming the fixture, the logical symbol and both answers. Property 33 is implemented here. _Requirements: 2.19, 34.16. Design: Cross-language agreement._

- [ ] 8. Contract reference documentation for a reader outside this fleet
  - [~] 8.1 Write the Contract's reference pages: every issue kind with its rule, confidence, default and fixability; every exemption class with its detection rule; the exit-code table; the edge form; the pending state; the migration notes carrying the `EU1001` and `EU1002` mapping.
    - Verify: the pages are readable with no access to any first-party source, and every claim cites the Contract file that carries it. _Requirements: 1.6, 1.13, 2.6, 35.1, 35.2, 35.8. Design: The Contract._
  - [~] 8.2 Write the documentation coverage test: every code in `kinds.json` and every class in `exemptions.json` has a documented rule, confidence, default and fixability.
    - Verify: the test fails when a kind row is added with no documentation page. _Requirements: 35.1, 35.2, 35.6. Design: The Contract._

- [~] 9. Checkpoint. THE Contract stands on its own
  - Ensure all tests pass, ask the user if questions arise. The corpus format, the schemas, the grammars and both vector sets are testable with no analyzer in existence; confirm that before either analyzer starts consuming them.

### `deadset-go`: the Go analyzer

- [ ] 10. Load, configuration and the report-only guarantee
  - [~] 10.1 Implement the loader: one `packages.Load` per build configuration with the design's `Need` bit set, `Tests: true`, `CGO_ENABLED=0`, one `FileSet` per configuration, a fail-closed walk of every package's `Errors`, and the cgo policy: a file the toolchain ignored solely for importing `"C"` is recorded in the report's `excluded_by_cgo` list and named as a declared limit, never treated as never built.
    - Verify: a fixture with a type error exits 3 printing every load error and no finding list; a fixture with an `//go:build plan9` file shows that file in `IgnoredFiles` and not in `GoFiles`; a fixture with a file importing `"C"` lists it in `excluded_by_cgo` and reports no `DS1501` for it; the binary runs on a host with no C toolchain. _Requirements: 26.4, 26.5, 26.7, 26.8, 28.1, 28.3, 28.9, 28.10. Design: The Go analyzer, Load._
  - [~] 10.2 Implement configuration decoding with `encoding/json` under `DisallowUnknownFields` against the closed key list of `config.schema.json`, the resolution order of flag then repository then central then default, and `print-config` with a `provenance` object naming the source of every setting.
    - Verify: every published configuration vector passes; the printed output read back as a repository configuration resolves to an equivalent configuration; a key outside the list fails the decode; no parser of any other syntax exists in the source. _Requirements: 27.1 to 27.6, 27.10 to 27.14, 27.16, 27.17. Design: Configuration, The configuration format, and why nothing parses it._
  - [~] 10.3 Implement the target-kind requirement and the unimplemented-key refusal, both with no inference and no default.
    - Verify: a run with neither source supplying `target.kind` exits 2 naming the field and the two sources searched; a near-miss key exits 2 naming the key and the nearest implemented key. _Requirements: 27.7 to 27.9, 27.15, 33.4. Design: Configuration, Error Handling._
  - [~] 10.4 Implement the report-only guard: no verb accepts a source-editing flag, and an invocation requesting one exits 2 naming the request.
    - Verify: `analyze --fix` exits 2 with the named request; a run over a fixture leaves every file's hash unchanged. _Requirements: 23.1 to 23.3, 23.6, 23.7, 34.11. Design: Components and Interfaces, Error Handling._
  - [~] 10.5 Property test: configuration resolution round-trips and the repository configuration wins.
    - Property 24. Validates Requirements 27.1 to 27.6, 27.10 to 27.13. Generators produce central and repository pairs plus flags; a comment anywhere changes no resolved value.
  - [~] 10.6 Property test: an unimplemented configuration key is named rather than ignored.
    - Property 25. Validates Requirement 27.15. The generator produces keys absent from the schema, including near-misses of implemented keys.
  - [~] 10.7 Property test: analysis leaves the tree byte-identical.
    - Property 23. Validates Requirements 23.1, 23.2, 23.3, 23.6, 34.11. The generator produces target trees; the assertion hashes every file before and after.
  - [~] 10.8 Property test: the declared target kind is the only thing that changes the target's treatment.
    - Property 14. Validates Requirements 27.7, 27.9, 33.9. Adding or removing an executable entry point changes no finding; changing the declared kind does.

- [ ] 11. Symbol inventory and identity
  - [~] 11.1 Implement the symbol model and the enumeration of the eleven Go symbol kinds, each with its parent symbol and its size in source lines.
    - Verify: a golden symbol table over a fixture holding every kind, including a struct field inside a live struct and an interface method. _Requirements: 4.1 to 4.5. Design: The graph._
  - [~] 11.2 Implement position keying: the raw `token.Pos` inside one configuration, the rendered target-relative `path:line:col` across configurations and in every report, and one declaration per source site across package variants.
    - Verify: a fixture with in-package tests produces one declaration per source line while `types.Object` identity differs across variants, asserted directly. _Requirements: 4.6, 7.7, 28.2. Design: Position keying, and the test-variant trap._
  - [~] 11.3 Implement the stable symbol reference emitter for every Go symbol form, from the module path and package directory.
    - Verify: the published symbol-ref token corpus round-trips, and a rename of a file above a declaration leaves the reference unchanged. _Requirements: 31.1, 31.2. Design: The stable symbol reference._
  - [~] 11.4 Property test: one declaration per source site.
    - Property 1. Validates Requirements 4.1, 4.6, 7.7, 28.2. The generator varies configuration count and package-variant count for one source file.

- [ ] 12. The reference pass
  - [~] 12.1 Implement the one-pass identifier walk carrying the enclosing declaration down the tree, resolving through `Uses`, `Selections` and `Implicits`, and recording `Reference.From`.
    - Verify: a golden reference table over a fixture holding a method selector, an embedded field selector, a type-switch binding and a self-reference; a symbol referenced only from its own declaration site carries zero references; the pass issues no per-symbol query. _Requirements: 5.1, 5.4, 5.7, 7.4, 28.5. Design: The graph._
  - [~] 12.2 Implement read and write classification from the identifier's syntactic position, keeping every write position per symbol.
    - Verify: a fixture covering assignment, compound assignment, increment, address-of and plain use produces the golden read and write split. _Requirements: 10.1, 10.7. Design: The graph._
  - [~] 12.3 Implement test-file classification by the Go language's own rule, recording the rule that produced each classification, plus production mode.
    - Verify: `test_file_rules` reports the matched count over a fixture; production mode drops every test reference from the reference set. _Requirements: 11.2, 11.6, 28.8. Design: The graph, Roots, entry points and test files._

- [ ] 13. Roots
  - [~] 13.1 Implement the eight detected root classes: `main`, every `init`, the test-function families, a `go:linkname` target, a cgo export, a blank-identifier declaration, and a library target's published API.
    - Verify: one fixture per class in which the root holds a symbol live, and one in which the same symbol with the root removed is reported. _Requirements: 3.2, 3.4, 3.8. Design: Roots._
  - [~] 13.2 Implement configured roots and root patterns, `print-roots`, and the `DS1704` finding for a configured root or pattern that matches nothing.
    - Verify: `print-roots` prints the resolved set; a configured root naming no symbol reports `DS1704` and exits 1. _Requirements: 3.1, 3.5 to 3.7. Design: Roots._
  - [~] 13.3 Implement the relation split: a root makes a symbol live under reachability and plays no part in reference counting, which asks only whether any declaration in the loaded graph references the symbol, recorded per finding.
    - Verify: a library fixture whose exported entry point is a root still reports that symbol under `DS1001` by reference counting while its transitive closure is not reported as a dead component; adding a root changes no reference-counting verdict in the fixture. _Requirements: 3.4, 5.1, 5.2, 5.3, 7.1. Design: Roots, Mark, sweep and components._

- [ ] 14. Mark, sweep and dead components
  - [~] 14.1 Implement reference counting as the non-recursive in-degree test (`in[s]` non-empty, from any declaration in the loaded graph, roots playing no part), reachability as the closure of the root set, and the suppression marks that make a symbol live under both before either runs, recording on each finding which relation produced it.
    - Verify: a fixture holding a symbol referenced only by an unreachable symbol reports it under reachability and not under reference counting, each finding naming its relation; a fixture in which a dead symbol is suppressed reports none of the symbols only it references. _Requirements: 5.1, 5.2, 5.3, 6.1, 21.18. Design: Mark, sweep and components, Suppression._
  - [~] 14.2 Implement Tarjan over the dead subgraph seeded with parent-to-member edges, the condensation DAG over its components, component roots, and the fall set per root (the component's members plus every dead symbol only that component reaches) as the reported symbol count and deletable line total, returned in reverse topological order; plus the `DS1005` rule, a test whose referenced target set is non-empty and wholly dead joins the dead set before Tarjan runs.
    - Verify: a fixture holding a cycle of mutually referencing dead symbols reports the component's roots; a root whose component alone reaches three further dead symbols reports a count and line total covering all of them; a test referencing only dead target symbols lands in their component and a test referencing one live target symbol is not reported; a dead type's members are inside the parent's component rather than independent findings. _Requirements: 4.4, 6.2 to 6.4, 6.6, 6.7, 9.7, 11.3, 24.8, 34.10. Design: Mark, sweep and components._
  - [~] 14.3 Implement the cascade output modes, including the full listing of every symbol in each component.
    - Verify: the full mode lists every member and the default mode lists the roots and the count. _Requirements: 6.5, 6.7. Design: Mark, sweep and components._
  - [~] 14.4 Property test: the liveness relation is recorded and is the one that produced the finding.
    - Property 7. Validates Requirements 3.4, 5.1, 5.2, 5.3, 6.1, 7.1. The generator produces graphs and root sets, plants symbols referenced only by unreachable symbols, and checks each finding's relation against the graph and that roots change no reference-counting verdict.
  - [~] 14.5 Property test: every dead symbol lands in exactly one component, reported at its roots.
    - Property 6. Validates Requirements 4.4, 4.5, 6.1 to 6.7, 9.7, 11.3, 24.8, 34.10. The generator produces chains, cycles, tests whose referenced targets are all dead, tests referencing a live target, and nested members, and checks the fall set against the condensation DAG.

- [~] 15. Checkpoint. The Go graph answers liveness
  - Ensure all tests pass, ask the user if questions arise. Confirm the measured variant behavior and the reference table before exemptions start suppressing findings, because a keying defect here reports live symbols at deny severity.

- [ ] 16. Exemptions
  - [~] 16.1 Implement the exemption framework: the class registry read from `exemptions.json`, the `retained_by` record, `print-retained`, and the per-class disable switch.
    - Verify: `print-retained` lists every held-back symbol with its classes; disabling a class grows the reported set and never shrinks it; no reported finding carries a non-empty `retained_by`. _Requirements: 7.5, 14.1, 14.2, 14.12, 14.13. Design: Exemptions._
  - [~] 16.2 Implement `interface-satisfaction` over the conversion set: every site where a value of a type is converted or assigned to an interface type, then `types.Implements` per pair.
    - Verify: the corpus fixture `interface-satisfaction-conversion` passes; a fixture with no conversion site reports the method; the staticcheck-style conservative form is shown retaining strictly more on the same fixture. _Requirements: 14.3, 28.6, 28.7, 34.7. Design: Exemptions._
  - [~] 16.3 Implement `encoding-reflection` and `format-verb-contract`: the flow set into reflection, the standard encoding packages, the template packages, a database scan target, a sort interface, a log-value interface and a formatting verb.
    - Verify: one firing and one non-firing fixture per named destination, with the retained set naming the type's exported methods and its tagged fields. _Requirements: 14.4, 28.6. Design: Exemptions._
  - [~] 16.4 Implement `errors-duck-typing` and `enum-group`: the four error-helper signatures on a type reachable as an error, and every member of an `iota` group whose type carries a conversion method.
    - Verify: one firing and one non-firing fixture per class; an enum member with no conversion method on its type is reported under `DS1302`. _Requirements: 10.5, 14.5, 28.6. Design: Exemptions._
  - [~] 16.5 Implement `generated-file` and `linkname-cgo-asm-plugin`, including the included-generated mode that reports findings marked `generated` and `fixability: none`.
    - Verify: a generated fixture retains by default and reports with generated files included, every such finding carrying `fixability: none`; one fixture per linkname, cgo export, assembly reference and plugin lookup. _Requirements: 14.6, 14.7, 14.11, 28.6. Design: Exemptions._
  - [~] 16.6 Implement `template-field` and `reflective-lookup` at the lowest confidence, each naming the site it found.
    - Verify: a configured template directory retains the referenced field and names the template position; a string literal near a `MethodByName` call retains the method and names the call site; with no template directory configured neither fires. _Requirements: 14.9, 14.10. Design: Exemptions._
  - [~] 16.7 Property test: the reported set and the retained set are disjoint.
    - Property 4. Validates Requirements 7.5, 14.1, 14.2, 14.12, 14.13. The generator plants exemptions across a symbol set and toggles class disablement.
  - [~] 16.8 Property test: interface satisfaction retains exactly the satisfying methods.
    - Property 5. Validates Requirements 9.6, 14.3, 28.7, 34.7. The generator produces concrete types, interfaces and conversion sites in arbitrary combinations.

- [ ] 17. Build configurations and matrix intersection
  - [~] 17.1 Implement matrix derivation: the platform and custom tag atoms present in the target tree plus the host platform, with no enumeration of tag combinations, and the `matrix.complete` declaration that marks a configured matrix as every configuration the target builds, the derived matrix being incomplete by definition.
    - Verify: a fixture carrying three platform atoms derives four configurations and not the product of the atoms; a fixture with a Boolean constraint the atoms do not satisfy is recorded as unreachable by derivation; the resolved configuration reports `matrix.complete` as false unless set. _Requirements: 15.1, 15.5, 15.8. Design: Build configurations and intersection._
  - [~] 17.2 Implement per-configuration loading and the intersection: report only what is dead everywhere, count a reference that holds anywhere, analyze a declaration that exists anywhere, and name the configurations per finding.
    - Verify: the corpus multi-configuration fixture passes; a symbol used under one configuration only is not reported. _Requirements: 15.2 to 15.4, 15.6, 34.9. Design: Build configurations and intersection._
  - [~] 17.3 Implement the fail-closed rule for a configuration that does not load.
    - Verify: a fixture whose second configuration fails to load exits 3 naming the configuration identifier and prints no intersection. _Requirements: 15.7, 26.4. Design: Build configurations and intersection, Error Handling._

- [ ] 18. Consumers, confidence and severity
  - [~] 18.1 Implement the scope document reader and consumer loading from an already-populated workspace or an explicit scope, with declarations scoped to the target and references counted from every loaded module, and no network request at any point.
    - Verify: the corpus fixture `unused-exported-consumer` passes; an absent consumer path exits 3 naming the path; a consumer that fails to load exits 3; a network-blocked run produces the same report. _Requirements: 12.1 to 12.7. Design: Visibility, reachability class and consumers._
  - [~] 18.2 Implement reachability-class derivation from consumer availability and symbol visibility, with the loaded consumer set named per finding.
    - Verify: three fixtures produce `certain`, `probable` and `possible` respectively, each naming its loaded consumers. _Requirements: 12.8 to 12.11, 13.1 to 13.4. Design: Visibility, reachability class and consumers._
  - [~] 18.3 Implement confidence as the reachability class capped by the kind's declared ceiling, and the `--min-confidence` filter over it.
    - Verify: an unused enum member and an unused function type parameter in one fixture report `confidence: certain` equal to their class; with a ceiling of `probable` injected by the test, since no shipped kind declares one below `certain`, a `certain`-class finding reports `confidence: probable`, and a run at `--min-confidence=certain` omits it and reports the omission count. _Requirements: 10.4, 10.6, 13.1, 13.5, 13.7, 24.9. Design: Confidence and the reachability class._
  - [~] 18.4 Implement the per-kind severity map with allow, warn and deny, the library defaults of Requirements 13.10 and 13.11, and the range-prefix form that disables a family.
    - Verify: a library fixture with no consumer information defaults `DS1001` off and the narrowing kinds on over its `internal/` tree and `main` packages with no narrowing finding on its published API; `"DS18": "allow"` disables the whole intra-function family; a configuration naming `DS1703` under a severity key exits 2. _Requirements: 13.6, 13.8, 13.9, 13.10, 13.11, 20.1, 21.11, 27.15. Design: Issue kinds, Configuration._
  - [~] 18.5 Property test: a reference from any loaded module prevents the finding.
    - Property 3. Validates Requirements 7.1, 9.4, 12.1, 12.11, 34.6. The generator places references in the target and in consumers and checks the named consumer set exactly.
  - [~] 18.6 Property test: the class and the severity are independent dials.
    - Property 13. Validates Requirements 13.6, 13.8. Changing the severity map changes no class; changing the consumer set or target kind changes no severity.

- [ ] 19. Declarations, test-only use, deprecation and visibility narrowing
  - [~] 19.1 Emit `DS1001`, `DS1002` and `DS1003`, with a symbol referenced only from its own declaration site routed to the code its visibility selects and reported once.
    - Verify: one firing and one non-firing fixture per code; a self-referencing symbol appears under exactly one code. _Requirements: 7.1 to 7.4, 7.6, 7.7, 34.5. Design: Issue kinds._
  - [~] 19.2 Emit `DS1004` and `DS1005`: a symbol with zero production and at least one test reference, and a test whose referenced target set is non-empty and wholly dead, the message stating that rule, both placed in the same component.
    - Verify: moving one reference from a test file to a production file removes the `DS1004` finding; a test referencing two dead target symbols reports `DS1005` and shares their component identifier; a test referencing one dead and one live target symbol reports nothing; a consumer's test reference counts as a test reference by default and as production when configured. _Requirements: 6.6, 10.3, 11.1 to 11.5. Design: Issue kinds, Mark, sweep and components._
  - [~] 19.3 Emit `DS1006` from the deprecation marker the standard Go tools recognize, reported under the most specific code and never twice.
    - Verify: a deprecated unreferenced symbol reports `DS1006` alone; a deprecated symbol with a production reference reports nothing. _Requirements: 19.1, 19.2, 7.7. Design: Issue kinds._
  - [~] 19.4 Emit `DS1101`, `DS1102` and `DS1103` from the reference set's package spread under the closed-world precondition, each naming the narrower visibility the references support and classified behavior-preserving, with a declared cross-language edge counted as an out-of-package reference.
    - Verify: a published package with `consumers.complete` unset reports no `DS1101` or `DS1102` while the same module's `internal/` tree and `main` packages do; with the flag set and every declared consumer loaded the published API is reported; a `DS1101` candidate whose only outside reference is a declared edge is published as an edge evaluation and not as a finding; `DS1103` reaches `certain` with no consumer information at all, over a `package main`, a `_test` package and an `internal` tree; each finding carries `details.narrower_visibility`. _Requirements: 8.1 to 8.3, 8.5 to 8.8, 12.13, 13.2. Design: Visibility, reachability class and consumers._
  - [~] 19.5 Property test: visibility narrowing names the widest scope containing every reference.
    - Property 10. Validates Requirements 8.1 to 8.5. The generator varies declared visibility against reference placement across files, packages and modules.

- [ ] 20. Interfaces
  - [~] 20.1 Emit `DS1201`: an interface no symbol uses as a type, naming its implementations and their positions.
    - Verify: one firing and one non-firing fixture; an interface used only by a loaded consumer is not reported; an interface with exactly one implementation and a use as a type is not reported under any code. _Requirements: 9.1, 9.4, 9.6. Design: Issue kinds._
  - [~] 20.2 Emit `DS1203`: an interface method no call site invokes or selects through the interface, whatever the implementation count, exempting every method of an interface that declares an unexported method and every marker method whose implementations all carry an empty body.
    - Verify: a fixture where the concrete method is called directly but never through the interface reports the interface method and not the concrete one; a sum-type interface with an unexported method reports nothing; a marker method with empty-bodied implementations reports nothing; a method selected through the interface as a value counts as invoked. _Requirements: 9.3, 9.6. Design: Issue kinds._
  - [~] 20.3 Emit `DS1204` and place an unused interface's members inside the interface's component.
    - Verify: a `var _ I = T{}` assertion whose interface nothing uses as a type reports `DS1204` while still retaining the concrete methods under `interface-satisfaction`; an unused interface's members produce no independent `DS1003`. _Requirements: 9.5, 9.7. Design: Issue kinds, Exemptions._

- [ ] 21. Reads, writes, enumerated members and type parameters
  - [~] 21.1 Emit `DS1301` with every write position named, honoring production mode for the read side.
    - Verify: a symbol written in production and read only from a test reports under production mode and not otherwise; `details.write_positions` equals the fixture's write set. _Requirements: 10.1, 10.2, 10.3, 10.7. Design: Issue kinds, The graph._
  - [~] 21.2 Emit `DS1302`, enabled by default with no confidence ceiling, interacting with the `enum-group` exemption.
    - Verify: a member of a group whose type carries no conversion method and is produced by no conversion is reported at the derived class, `certain` for an unexported type; a member of a group whose type carries a conversion method, or is produced by a conversion from an integer or a wire value, is retained and listed by `print-retained`. _Requirements: 10.4, 10.5. Design: Issue kinds, Exemptions._
  - [~] 21.3 Emit `DS1303` for function and method type parameters only, enabled by default at `certain`, with type declarations exempt.
    - Verify: a function type parameter that neither the signature nor the body names is reported at `certain` with `fixability: deletable`; a type parameter named only in the body, and one named only in a result type, each report nothing; the phantom type `type ID[T any] int` reports nothing. _Requirements: 10.6, 13.5. Design: Issue kinds._

- [ ] 22. Non-code artifacts and dependencies
  - [~] 22.1 Emit `DS1501` and `DS1502`: a file no configuration builds with the constraint that excluded it, reported only under a matrix the configuration declares complete and never for a file cgo alone excluded, and a file no import reaches and no root names.
    - Verify: an `//go:build plan9` file reports `DS1501` with `details.excluded_by` naming the constraint when `matrix.complete` is set and reports nothing under the derived matrix; a file importing `"C"` reports nothing and appears in `excluded_by_cgo`; a firing and a non-firing fixture for `DS1502`. _Requirements: 15.8, 16.1 to 16.3, 28.10. Design: Non-code artifacts and dependencies, Load._
  - [~] 22.2 Emit `DS1601` with `go mod tidy -diff`'s semantics exactly: a direct `require` whose module provides no package any target package or test variant imports, read from `go mod edit -json` on the toolchain the load already spawned, with an `// indirect` requirement never reported; and assert that a missing requirement and an unresolvable import specifier exit 3 through the load rather than producing a finding.
    - Verify: one firing and one non-firing fixture for `DS1601`; a `go.mod` carrying `// indirect` requirements reports none of them; the finding set equals the requirements `go mod tidy -diff` would remove on the same fixture; a fixture importing a module `go.mod` does not require exits 3 with the load error and no finding list. _Requirements: 17.1, 26.4, 28.9. Design: Non-code artifacts and dependencies._
  - [~] 22.3 Emit `DS1605` for a `replace` directive whose target module is absent from the build list, read from `go mod edit -json`, and implement the `removes_last_use_of` join on any deletion candidate whose removal drops a dependency's last use.
    - Verify: a `replace` whose target is absent from the build list reports `DS1605` and one whose target is in the build list reports nothing; an `exclude` directive, a `go.work` `use` entry and a `tool` directive each report nothing; a deletion candidate whose references are a module's only uses names that module in `details.removes_last_use_of`. _Requirements: 17.4, 17.5, 28.9. Design: Non-code artifacts and dependencies._

- [ ] 23. Suppression, staleness and the self-check kinds
  - [~] 23.1 Implement the inline directive in both comment spellings, scoped to the line above the affected declaration, and `DS1701` for one carrying no reason.
    - Verify: the published suppression token corpus passes; a directive with no reason reports `DS1701` and exits 1; a directive two lines above the declaration matches nothing. _Requirements: 21.1 to 21.3, 21.5, 21.15. Design: Suppression._
  - [~] 23.2 Implement `deadset-ignore.json`, decoded with `encoding/json` under `DisallowUnknownFields`, matching on code, symbol reference and path together, `DS1702` for an entry naming a symbol with no path, and `DS1701` for an entry whose required `reason` is absent or empty.
    - Verify: a same-named symbol in another file and another package is not suppressed by an entry naming the first; a bare-name entry reports `DS1702`; an entry with no `reason` reports `DS1701` and exits 1. _Requirements: 21.4 to 21.7, 21.18, 27.10. Design: Suppression._
  - [~] 23.3 Implement `deadset-baseline.json` as a ratchet: written by the analyzer with the required `reason` on every row carrying the analyzer's provenance, read back to suppress exactly the recorded findings, adjudicating nothing.
    - Verify: a written baseline read back on an unchanged tree exits 0; a new finding absent from the baseline exits 1; every written row carries a non-empty `reason` naming the analyzer version and run. _Requirements: 21.8, 21.14, 21.18. Design: Suppression._
  - [~] 23.4 Implement staleness through `Suppression.matched`, bound before the sweep from the mark that made the suppressed symbol live, and emit `DS1703` for every suppression whose mark bound to nothing the sweep would have reported, at either mechanism and in the baseline, with the counts in `totals`.
    - Verify: a stale entry at each of the three mechanisms exits 1; no flag, severity setting or per-mechanism exception reduces it, and a configuration attempting one exits 2; `totals.suppressions_in_effect` and `totals.reasons_recorded` count only bound suppressions; a suppression on a dead component's root leaves the symbols only that root references unreported. _Requirements: 21.9 to 21.11, 21.13, 25.9. Design: Suppression, Data Models._
  - [~] 23.5 Implement own-side edge reading from `deadset-edges.json`, one `edge_evaluations` record per edge side this analyzer enumerated carrying the state and, when dead, the finding, the same record for a narrowing candidate whose only outside reference is an edge, and `DS1705` for an edge naming a symbol this analyzer does not enumerate.
    - Verify: a fixture with a declared edge and no local reference publishes one evaluation with state `dead` carrying the finding, lists that finding nowhere else, and exits 4; a `DS1101` candidate whose only outside reference is the edge is published the same way; the analyzer never resolves the paired symbol. _Requirements: 8.7, 25.8, 31.4, 31.5, 31.9, 31.11, 34.19. Design: The cross-language edge, and the pending finding._
  - [~] 23.6 Property test: anything the configuration names that matches nothing is reported and fails the run.
    - Property 17. Validates Requirements 3.6, 3.7, 21.5, 21.9, 21.10, 31.9. The generator produces suppressions, configured roots, root patterns and edges that match nothing.

- [ ] 24. Reporters, exit codes and explanations
  - [~] 24.1 Implement the text and JSON reporters over one ordered finding slice.
    - Verify: golden text and JSON output over the whole fixture set; every text line matches the published format regular expression; the JSON decodes into the Contract's typed report shape with unknown fields refused, and the committed goldens validate against the schemas in `deadset-spec`'s own continuous integration rather than in this suite. _Requirements: 24.1, 24.2, 34.3. Design: Reporters and performance, The text line format._
  - [~] 24.2 Implement the GitHub Actions annotation reporter and the user-supplied template reporter.
    - Verify: one error annotation per failing finding and one per stale suppression; a template naming an absent field fails rather than rendering empty. _Requirements: 24.3, 24.5, 33.5, 33.7. Design: Reporters and performance._
  - [~] 24.3 Implement the SARIF 2.1.0 reporter to the published mapping, omitting suppressed findings and carrying both fingerprint keys.
    - Verify: the output validates against the SARIF schema; a suppressed finding is absent from the document while `totals.suppressions_in_effect` counts it; `partialFingerprints` carries `primaryLocationLineHash` and `deadsetSymbolRef/v1`, and the second is unchanged by a line move. _Requirements: 24.4. Design: The SARIF 2.1.0 mapping._
  - [~] 24.4 Implement the canonical order, the configurable sort by position and by size, the run's deletable-line total, and `--max-findings` with the omission count.
    - Verify: the default order matches the canonical key; no timestamp, duration or host detail appears in the text format; the printed count plus the omitted count equals the finding count, and the deletable-line total covers the whole set rather than the printed subset. _Requirements: 24.6 to 24.9. Design: Reporters and performance._
  - [~] 24.5 Implement the exit-code table, with a warn finding not failing the run and the configured-off mode still printing the report.
    - Verify: one run per code from 0 to 4 against a fixture built for it. _Requirements: 25.1 to 25.9, 13.9. Design: The exit-code table._
  - [~] 24.6 Implement `explain` with its three questions, answered from the same analysis the report uses.
    - Verify: a live symbol prints a shortest path of real references from a real root; a retained symbol prints its classes; a reported symbol prints its class, loaded consumers and configurations; an unknown symbol exits 2 printing partial matches. _Requirements: 3.8, 22.1 to 22.5. Design: Components and Interfaces._
  - [~] 24.7 Property test: the report order is a total order with no ambient input.
    - Property 19. Validates Requirements 24.6, 24.7. The generator presents one finding set in arbitrary orders.
  - [~] 24.8 Property test: a capped report accounts for everything it omits.
    - Property 20. Validates Requirements 24.8, 24.9. The generator varies the finding count against the cap.
  - [~] 24.9 Property test: the exit code is a function of the report and the severity map.
    - Property 21. Validates Requirements 13.9, 21.10, 25.1, 25.2, 25.5 to 25.9. The generator varies findings, severities, pending count and stale count.
  - [~] 24.10 Property test: an explanation agrees with the report.
    - Property 30. Validates Requirements 3.8, 5.3, 22.1, 22.2, 22.3, 22.5. Exactly one explanation applies per symbol and it matches the finding.

- [ ] 25. Determinism, performance and the conformance run
  - [~] 25.1 Implement the determinism guarantees: no cache written between runs, no time budget, no truncation, no sharding.
    - Verify: two consecutive runs over a fixture produce byte-identical bytes; a run with an empty cache directory matches a run with a populated one; no code path writes analysis state to disk. _Requirements: 26.1 to 26.3, 34.4. Design: Reporters and performance._
  - [~] 25.2 Implement the per-package concurrency with a channel semaphore acquired in a `select` on the run context, writing into a result slice pre-filled by package index, and assert the dependency budget.
    - Verify: `go version -m` on the built binary lists nothing outside the standard library, `golang.org/x/tools` and the modules `x/tools` itself links, and `go.mod` declares no direct requirement outside `x/tools` and the test-only `rapid`; a stress run with the semaphore at one and at the core count produces identical bytes. _Requirements: 26.1, 28.4. Design: Reporters and performance, `sync.WaitGroup` with a channel semaphore rather than `errgroup`._
  - [~] 25.3 Calibrate and record the performance baseline: the measured runtime on a 21,441-line module against the prototype's 0.9 seconds, and a synthesized module of approximately 250,000 lines against the 60-second budget on a four-core runner.
    - Verify: a committed benchmark record naming both numbers, the machine, the module line count and the configuration count, re-run once per release and compared against the record. _Requirements: 26.6. Design: Reporters and performance, Testing Strategy._
  - [~] 25.4 Run THE Conformance_Corpus inside the Go test suite and commit `conformance.json` naming the corpus version and every declined capability with its reason.
    - Verify: the suite passes the corpus; the printed result names fixtures, passes, gaps and failures; the result and its digest appear in `analyzer.conformance` in every report. _Requirements: 2.16 to 2.18, 30.14, 34.14, 34.15. Design: The Conformance Corpus, Declared gaps._
  - [~] 25.5 Assert the single-analyzer pending behavior: a run alone over a fixture holding a declared edge exits 4 rather than 0.
    - Verify: the assertion runs `deadset-go analyze` directly, with no orchestrator, and checks the exit code and the named pending count. _Requirements: 25.8, 34.19. Design: The cross-language edge, and the pending finding._
  - [ ]* 25.6 Benchmark the analysis against the retired tool's measured runtime on the same module, for the migration notes.
    - Verify: a recorded comparison on one fleet module. No requirement obliges this leaf; it exists to give the migration guide a number.

- [ ] 26. The Go intra-function group
  - [~] 26.1 Emit `DS1801`, `DS1802`, `DS1803`, `DS1807` and `DS1809` over the graph, lifting the `unusedparams`, `unparam`, `ineffassign` and SA4020 rules, with `unparam`'s exemptions applied by name: an exported function of a target whose consumer set is not declared complete, a method that satisfies an interface, a function used as a value, a `go:linkname` or cgo target, and a stub whose body is empty or only panics are never reported, and `DS1803` reports only when every call site is in the loaded graph.
    - Verify: one firing and one non-firing fixture per code, the non-firing `DS1801` cases being a method retained by `interface-satisfaction` and an exported function in a library with `consumers.complete` unset; a `DS1803` candidate with a caller outside the loaded graph reports nothing; each message names its overlap from `kinds.json`; a suppression on an intra-function finding behaves as on any other. _Requirements: 20.2, 20.4 to 20.7. Design: The intra-function group._
  - [~] 26.2 Emit `DS1805` by running `golang.org/x/tools/go/analysis/passes/unreachable` over each package's syntax and type information, mapping each diagnostic to a finding at the enclosing declaration.
    - Verify: one firing and one non-firing fixture; the finding's position equals the pass's diagnostic position; `go version -m` on the built binary still lists nothing outside the standard library, `golang.org/x/tools` and the modules it links. _Requirements: 20.2, 20.6, 28.4. Design: The intra-function group._
  - [~] 26.3 Wire the group's one enable switch to the `DS18` range prefix of task 18.4, enabled by default.
    - Verify: a fresh configuration with no severity section reports all six kinds; disabling one kind by code leaves the other five reported. _Requirements: 20.1, 20.3. Design: The intra-function group, Configuration._

- [ ] 27. Documentation for a reader outside this fleet
  - [~] 27.1 Write the Go analyzer's reference documentation: every kind it implements, every exemption class, the reachability classes, the reference-versus-reachability difference naming `golang.org/x/tools/cmd/deadcode`, the Contract version, its declared gaps, the platform set and the non-goals.
    - Verify: the documentation names no host, workflow or internal vocabulary of this fleet, and every exported symbol carries a doc comment. _Requirements: 1.13, 5.6, 23.8, 35.1 to 35.7, 35.9 to 35.11. Design: Migration, Design decisions and alternatives._
  - [~] 27.2 Write the documentation coverage test: every implemented kind and class has a page, and every declared gap is listed.
    - Verify: the test fails when a kind is implemented with no documentation entry. _Requirements: 35.1, 35.2, 35.6. Design: Testing Strategy._

- [~] 28. Checkpoint. `deadset-go` is complete and conformant
  - Ensure all tests pass, ask the user if questions arise. The corpus run, the golden output and the fixture pairs are green here, before the measurement run treats the fleet as the oracle.

### `deadset-ts`: the TypeScript analyzer

- [ ] 29. The client boundary, project discovery and configuration
  - [~] 29.1 Implement the run lifetime: one `API`, one `Snapshot` opened with `updateSnapshot({ openProjects })`, disposed once, with the rule that a `NodeHandle` or a `Symbol` never reaches another project's checker.
    - Verify: a run over a two-project fixture creates one snapshot and disposes it once, asserted through the API's own accounting; a test that passes a handle across projects fails. _Requirements: 26.9, 29.1, 29.4. Design: The client boundary, which shapes everything else._
  - [~] 29.2 Implement project discovery from the explicit scope, every `tsconfig*.json` under the target root and each config's `references`, with `parseConfigFile` and a fail-closed read of the three diagnostic sets per project.
    - Verify: a fixture with a project-references graph discovers every project; a fixture with a semantic error exits 3 printing the diagnostics and no finding list; a package whose sources ship as TypeScript analyzes with no build step. _Requirements: 26.4, 26.5, 29.1, 29.9, 29.11. Design: Project discovery._
  - [~] 29.3 Implement configuration decoding with `JSON.parse` followed by structural validation against the closed key list of `config.schema.json`, the resolution order, `print-config` with its `provenance` object, the target-kind requirement and the unimplemented-key refusal.
    - Verify: every published configuration vector passes byte-exactly, which is the anti-drift pin against the Go decoder; a missing target kind exits 2 naming the field; a key outside the list exits 2 naming it. _Requirements: 27.1 to 27.17, 33.4. Design: The configuration format, and why nothing parses it._
  - [~] 29.4 Implement the report-only guard and the byte-identical assertion over a fixture run.
    - Verify: a verb requesting an edit exits 2 naming the request; every fixture file's hash is unchanged after a run. _Requirements: 23.1 to 23.3, 23.6, 23.7, 34.11. Design: Components and Interfaces._

- [ ] 30. Symbol inventory, members and identity
  - [~] 30.1 Implement the module and file symbol inventory: `getExports` on each module symbol plus a walk of each source file's statements for non-exported declarations, excluding external-library and default-library files.
    - Verify: a golden symbol table over a fixture holding every declaration form in Requirement 4.3; no symbol from `node_modules` or `lib.d.ts` appears. _Requirements: 4.1 to 4.3, 4.5. Design: Symbols._
  - [~] 30.2 Implement member enumeration through `getMembers` and `getExports` per container, with visibility read from `ModifierFlags` and the `private` versus `#private` distinction recorded for the reflective exemptions.
    - Verify: a fixture holding a private, protected, static, accessor and `#private` member enumerates all five, one call per container rather than one per member; the reflective classes apply to `private` and not to `#private`. _Requirements: 4.3, 4.4, 29.2, 29.5, 29.6. Design: Members, which is the capability nothing else has._
  - [~] 30.3 Implement the `ts://` stable symbol reference and the cross-project position key.
    - Verify: the published symbol-ref token corpus round-trips; the cross-project comparison is on rendered positions and never on handles. _Requirements: 4.6, 31.1, 31.2. Design: The stable symbol reference, The client boundary._
  - [~] 30.4 Property test: the stable symbol reference survives an edit above it.
    - Property 32. Validates Requirements 21.6, 21.12, 31.1. The generator edits lines above a declaration without touching its name or container; the reference and its suppression both hold.
  - [~] 30.5 Property test: language namespacing keeps identically named symbols distinct.
    - Property 28. Validates Requirements 31.2, 31.3. The generator produces same-named symbol pairs across the two languages and checks both identifiers and the per-finding language field.

- [ ] 31. The reference pass and its batching
  - [~] 31.1 Implement the local `forEachChild` walk carrying the enclosing declaration, and resolve each file's identifier array through the batched `getSymbolAtLocation`.
    - Verify: a golden reference table over a fixture holding property access, element access, a member reference and a self-reference; `getReferencesToSymbolInFile` and `getReferencedSymbolsForNode` appear nowhere in the source, asserted by a grep test. _Requirements: 5.1, 5.5, 5.7, 7.4, 29.2, 29.3, 29.8. Design: The client boundary, which shapes everything else._
  - [~] 31.2 Implement the residue fallbacks: `getResolvedSymbol` per node for what the batch leaves undefined, `getShorthandAssignmentValueSymbol` for a shorthand property assignment, and `getExportSymbol` so an alias resolves to the declaration it names.
    - Verify: a fixture holding a shorthand assignment, a re-export chain and a type-only re-export resolves every reference to the declaration rather than to the intermediate. _Requirements: 29.2, 29.7, 19.4. Design: The client boundary, Exports, re-exports and type-only exports._
  - [~] 31.3 Implement read and write classification, and test-file classification defaulting to the measured `**/*.test.{ts,tsx,mts,cts}` pattern with the rule recorded per file and the pattern configurable.
    - Verify: `test_file_rules` reports the matched count; a project whose tests use another naming is measured correctly once configured; production mode drops every test reference. _Requirements: 10.1, 11.2, 11.6, 11.7. Design: Roots, entry points and test files._
  - [~] 31.4 Property test: batching bounds the round-trip count.
    - Property 31. Validates Requirements 5.7, 29.3. The round trips are bounded by `file_batches + residue_fallbacks + pair_assignability_calls`, measured through `getTimingInfo()`, so the identifier pass does not grow with the identifier count and the satisfaction pass grows only with the conversion set.
  - [~] 31.5 Property test: the production and test reference split decides the kind.
    - Property 8. Validates Requirements 10.3, 11.1, 11.2, 11.4, 11.5. Moving any one reference from a test file to a production file removes the finding.
  - [~] 31.6 Property test: a write with no read is reported with its write positions.
    - Property 9. Validates Requirements 10.1, 10.2, 10.7. The generator varies the read and write mix across members and module-level bindings.

- [ ] 32. Batch-cap calibration
  - [~] 32.1 Build the timing harness on the `collectTiming` option and `getTimingInfo()`, reporting round-trip latency, bytes transferred and server processing time per cap value.
    - Verify: the harness reports all three numbers for one package at three cap values. _Requirements: 29.3. Design: A per-file batch capped at 4096, calibrated rather than guessed._
  - [~] 32.2 Sweep the cap across every TypeScript package in this fleet, including the 4320-line file the design names, and record the chosen default in the configuration schema's default column.
    - Verify: a committed calibration record naming the sweep values, the measured numbers per value, the chosen default, and per package the three terms of Property 31's bound, `file_batches`, `residue_fallbacks` and `pair_assignability_calls`; the cap remains configuration rather than a constant. _Requirements: 29.3. Design: A per-file batch capped at 4096, calibrated rather than guessed, Interface satisfaction and deprecation._

- [ ] 33. Roots, mark, sweep and components
  - [~] 33.1 Implement roots: the exports of each configured entry file, each configured binary, every entry point the manifest names through `main`, `module`, `types`, `bin` and `exports`, and a library target's published API; plus configured roots, patterns, `print-roots` and `DS1704`.
    - Verify: a fixture whose manifest names four entry-point fields roots all four; a configured root matching nothing reports `DS1704` and exits 1. _Requirements: 3.1, 3.3 to 3.8. Design: Roots, entry points and test files._
  - [~] 33.2 Implement the five lifted entry-point rules, each from the corresponding `knip` plugin: the test runner's configuration and test files (`vitest`), the lint configuration and its imports (`eslint` flat config), the mutation-testing configuration (`stryker`), the browser-test configuration (`playwright`), and a worker or service worker addressed by a string literal the program's module resolution maps to a file; every other convention of the plugin corpus is recorded as a declared gap naming the plugin.
    - Verify: a fixture carrying `vitest.config.ts`, `eslint.config.mjs`, `vitest.stryker.config.ts`, `playwright.config.ts` and a `navigator.serviceWorker.register('./sw.ts')` call roots all five files and reports no `DS1502` for them; a worker addressed by a computed string is not rooted and is reported; the declared-gap list names at least one plugin the analyzer does not lift. _Requirements: 3.9, 16.2. Design: Roots, entry points and test files._
  - [~] 33.3 Implement the graph, reference counting as the non-recursive in-degree test with roots playing no part, reachability from roots, suppression marks before both, and the per-finding relation record.
    - Verify: the corpus fixtures that exercise both relations pass; a symbol referenced only by an unreachable symbol is live under reference counting and dead under reachability, and the finding records the producing relation. _Requirements: 5.1 to 5.3, 6.1, 21.18. Design: The rest is the same algorithm, Mark, sweep and components._
  - [~] 33.4 Implement Tarjan components with parent-to-member edges, the condensation DAG, component roots, the fall set as the reported count and line total, the `DS1005` rule and the cascade output modes.
    - Verify: the corpus component fixtures pass; a dead class's members sit in the parent's component; a test whose referenced targets are all dead shares their component and a test referencing a live target is not reported; a root's count covers every dead symbol only its component reaches. _Requirements: 4.4, 4.5, 6.2 to 6.7, 9.7, 11.3, 24.8. Design: The rest is the same algorithm, Mark, sweep and components._

- [ ] 34. Exemptions
  - [~] 34.1 Implement the exemption framework: the class registry read from `exemptions.json`, `retained_by`, `print-retained` and the per-class disable switch.
    - Verify: `print-retained` lists every held-back symbol with its classes; no reported finding carries a non-empty `retained_by`. _Requirements: 7.5, 14.1, 14.2, 14.12, 14.13, 29.10. Design: Exemptions (TypeScript)._
  - [~] 34.2 Implement satisfaction through `isTypeAssignableTo` over the conversion set, with class instance types resolved in batches through `getTypeOfSymbol`'s array overload and, because `getDeclaredTypeOfSymbol` and `isTypeAssignableTo` carry no array overload in `typescript@7.0.2`, one round trip per interface for its declared type and one per `(class, interface)` pair for assignability, the pair count recorded in the calibration record.
    - Verify: a fixture where a class flows into an interface-typed position by assignment, argument, return and container element retains exactly the required members; a class with no such site reports them; the measured round-trip count for the pass equals the interface count plus the pair count, asserted through `getTimingInfo()`. _Requirements: 9.6, 14.1, 29.2, 29.3, 29.10. Design: Interface satisfaction and deprecation._
  - [~] 34.3 Implement `decorator` and `injection-container`.
    - Verify: one firing and one non-firing fixture per class; a decorated member is retained and the decorator's own symbol counts as a reference to it. _Requirements: 14.8, 29.10. Design: Exemptions (TypeScript)._
  - [~] 34.4 Implement `framework-lifecycle` and `serialization-contract`.
    - Verify: one firing and one non-firing fixture per class; a class flowing into `JSON.stringify` retains its data members and not its methods. _Requirements: 14.8, 29.10. Design: Exemptions (TypeScript)._
  - [~] 34.5 Implement `template-field` and `reflective-lookup` under the same class names the Go analyzer uses, at the lowest confidence, each naming its site.
    - Verify: the shared corpus expectation naming `retained_by = ["template-field"]` binds both renderings; a `#private` field is not retained by either class. _Requirements: 14.9, 14.10. Design: Exemptions (TypeScript), Members, which is the capability nothing else has._

- [ ] 35. The compiler-configuration matrix, consumers, confidence and severity
  - [~] 35.1 Implement one build configuration per compiler configuration and the intersection: report only what is dead in every project, count a reference holding in any, and name the configurations per finding.
    - Verify: a fixture whose symbol is used under one `tsconfig.json` only is not reported; a project that fails to load exits 3 and prints no intersection. _Requirements: 15.1 to 15.7, 29.9, 34.9. Design: The compiler-configuration matrix._
  - [~] 35.2 Implement consumer loading from a workspace or an explicit scope with no network request, declarations scoped to the target, and the reachability-class derivation with the loaded consumer set named per finding.
    - Verify: the corpus consumer fixture's TypeScript rendering passes; an absent or failing consumer path exits 3. _Requirements: 12.1 to 12.11, 13.1 to 13.4. Design: Visibility, reachability class and consumers._
  - [~] 35.3 Implement confidence as the class capped by the kind's ceiling, `--min-confidence`, the per-kind severity map and the library defaults.
    - Verify: a library package with no consumer information defaults `DS1001` off and the narrowing kinds on over files no manifest export reaches, with no narrowing finding on the published API; a configuration naming `DS1703` under a severity key exits 2. _Requirements: 13.5 to 13.11, 21.11, 27.15. Design: Confidence and the reachability class._
  - [~] 35.4 Property test: matrix intersection reports only what is dead everywhere.
    - Property 11. Validates Requirements 15.1 to 15.4, 15.6, 29.9, 34.9. The generator produces matrices of arbitrary size with per-configuration reference sets.
  - [~] 35.5 Property test: confidence is the derived class capped by the kind's ceiling.
    - Property 12. Validates Requirements 10.4, 10.6, 12.8 to 12.10, 13.1 to 13.5, 13.7. The generator varies visibility, target kind, consumer availability and issue kind; no TypeScript kind's ceiling sits below `certain`, so the generator draws the ceiling to exercise the cap arm, since no shipped kind declares one below `certain`.

- [ ] 36. The issue kinds
  - [~] 36.1 Emit `DS1001`, `DS1002` and `DS1003`, including class, interface, type-alias, enum and namespace members, and a private member with no reference in its own file at `certain`.
    - Verify: one firing and one non-firing fixture per code; the corpus fixture `private-member-unread` passes; a `#private` field with no reference is `certain` with no consumer information. _Requirements: 7.1 to 7.4, 7.6, 7.7, 13.2, 29.5, 29.6, 34.5. Design: Members, which is the capability nothing else has._
  - [~] 36.2 Emit `DS1004`, `DS1005` and `DS1006`, the last from `getJsDocTags` filtered for the deprecation tag.
    - Verify: a symbol reached only from a `*.test.ts` file reports `DS1004`; a deprecated unreferenced export reports `DS1006` alone. _Requirements: 11.1 to 11.5, 19.1, 19.3, 7.7, 29.2. Design: Interface satisfaction and deprecation._
  - [~] 36.3 Emit `DS1101`, `DS1103` and `DS1104` under the closed-world precondition, each naming the narrower visibility and classified behavior-preserving, with a declared cross-language edge counted as an out-of-file reference.
    - Verify: an export used only inside its own non-entry file reports `DS1104` and not `DS1001` when the project's `consumers.complete` is set or no manifest export reaches the file, and reports nothing in an entry file or in a published file with the flag unset; a declaration in a file nothing can import reports `DS1103` at `certain`; a `DS1104` candidate whose only outside reference is a declared edge is published as an edge evaluation. _Requirements: 8.1, 8.3 to 8.8, 12.13, 29.7. Design: Exports, re-exports and type-only exports, Visibility, reachability class and consumers._
  - [~] 36.4 Emit `DS1201` and `DS1203`, with an unused interface's members placed in its component and the marker-method exemption applied to `DS1203`.
    - Verify: one firing and one non-firing fixture per code; the members produce no independent `DS1003`; an interface with one implementation and a use as a type is not reported under any code; an interface method whose every implementation has an empty body reports nothing. _Requirements: 9.1, 9.3, 9.4, 9.6, 9.7. Design: Issue kinds._
  - [~] 36.5 Emit `DS1301`, `DS1302` and `DS1303`, the last two enabled by default at `certain`, with the `enum-group` exemption over a numeric enum a conversion produces.
    - Verify: a private member written and never read reports `DS1301` with its write positions; an enum member nothing names is reported unless a `number` assertion, a reverse mapping or a decoded value typed as the enum produces the enum's type, in which case every member is retained; a function type parameter neither the signature nor the body names is reported at `certain`, and a type-alias or class type parameter named nowhere reports nothing. _Requirements: 10.1 to 10.7. Design: Issue kinds, Exemptions (TypeScript)._
  - [~] 36.6 Emit `DS1501`, only under a matrix the configuration declares complete, and `DS1502`.
    - Verify: a file no project includes reports `DS1501` naming what excluded it when `matrix.complete` is set and reports nothing otherwise; a file no import reaches and no manifest entry or lifted entry-point rule names reports `DS1502`; a firing and a non-firing fixture per code. _Requirements: 3.9, 15.8, 16.1 to 16.3. Design: Issue kinds, Roots, entry points and test files._
  - [~] 36.7 Emit `DS1601` from the manifest and the import closure, with a manifest `bin` entry never reported, and assert that an unresolvable import specifier exits 3 through the project's diagnostics rather than producing a finding.
    - Verify: one firing and one non-firing fixture across dependencies, development dependencies and peer dependencies; a manifest `bin` entry whose target no import reaches reports nothing; `removes_last_use_of` names a dependency a deletion would orphan; a fixture importing an undeclared package exits 3 with the diagnostic and no finding list. _Requirements: 17.1, 17.4, 17.6, 26.4. Design: Issue kinds._
  - [~] 36.8 Property test: an unreferenced declaration with no exemption is reported.
    - Property 2. Validates Requirements 7.1 to 7.4, 10.4, 19.1, 29.5 to 29.7, 34.5. The generator produces declarations at every visibility and container depth.

- [ ] 37. Suppression, staleness and the self-check kinds
  - [~] 37.1 Implement the inline directive in both comment spellings and `deadset-ignore.json`, decoded with `JSON.parse` plus the structural check, matching on code, symbol and path, with `DS1701` for a missing `reason` and `DS1702` for a bare name.
    - Verify: the published suppression token corpus passes byte-for-byte against the Go analyzer's result; a bare-name entry reports `DS1702`, which is what the `ignoreMembers` entries covering 60 and 23 files cannot become; an entry with no `reason` reports `DS1701`. _Requirements: 21.1 to 21.7, 21.15, 21.18, 27.10. Design: Suppression._
  - [~] 37.2 Implement `deadset-baseline.json` as a ratchet, written with the analyzer's provenance in every row's required `reason` and read back.
    - Verify: a written baseline read back on an unchanged tree exits 0; a new finding exits 1; every row carries a non-empty `reason`. _Requirements: 21.8, 21.14, 21.18. Design: Suppression._
  - [~] 37.3 Implement staleness through marks bound before the sweep, `DS1703` with no escape hatch, and the suppression and reason counts in `totals`.
    - Verify: a stale entry at each mechanism exits 1; no setting reduces it; the counts cover only bound suppressions; a suppressed member's dependents are not reported. _Requirements: 21.9 to 21.13, 25.9. Design: Suppression, Data Models._
  - [~] 37.4 Implement own-side edge reading from `deadset-edges.json`, one `edge_evaluations` record per edge side with the finding carried when the state is dead, the same record for a `DS1104` candidate whose only outside reference is an edge, `DS1705`, and exit 4 for a report holding an evaluation with a finding.
    - Verify: a fixture with a declared edge and no local reference exits 4 with the pending count named, lists the finding only inside the evaluation, and never resolves the paired symbol. _Requirements: 8.7, 25.8, 31.4, 31.5, 31.9, 31.11, 34.19. Design: The cross-language edge, and the pending finding._
  - [~] 37.5 Property test: a suppression document round-trips.
    - Property 15. Validates Requirements 21.2 to 21.4, 21.8, 21.12 to 21.14, 34.8. For each of the three mechanisms, writing the reported findings suppresses exactly those and reports no stale entry.
  - [~] 37.6 Property test: a suppression matches only what it names.
    - Property 16. Validates Requirements 21.6, 21.7. The generator produces same-named symbols across files and packages.

- [ ] 38. Reporters, exit codes and explanations
  - [~] 38.1 Implement the text and JSON reporters over one ordered finding slice.
    - Verify: golden text and JSON over the fixture set; every text line matches the published regular expression, so one grep expression matches both analyzers; the JSON decodes into the Contract's typed report shape with unknown fields refused, and the committed goldens validate against the schemas in `deadset-spec`'s own continuous integration. _Requirements: 2.12, 24.1, 24.2, 34.3. Design: The text line format._
  - [~] 38.2 Implement the annotation, template and SARIF reporters, with a suppressed finding omitted from the SARIF document and both fingerprint keys present.
    - Verify: the SARIF validates; a suppressed finding is absent while `totals.suppressions_in_effect` counts it. _Requirements: 24.3 to 24.5. Design: The SARIF 2.1.0 mapping._
  - [~] 38.3 Implement the canonical order, the configurable sort, the deletable-line total and `--max-findings` with the omission count.
    - Verify: the default order matches the canonical key; no timestamp, duration or host detail appears in the text format; printed plus omitted equals the total. _Requirements: 24.6 to 24.9. Design: Reporters and performance._
  - [~] 38.4 Implement the exit-code table.
    - Verify: one run per code from 0 to 4 against a fixture built for it. _Requirements: 13.9, 25.1 to 25.9. Design: The exit-code table._
  - [~] 38.5 Implement `explain` with its three questions from the same analysis the report uses.
    - Verify: each question answered over a fixture; an unknown symbol exits 2 with partial matches. _Requirements: 22.1 to 22.5. Design: Components and Interfaces._
  - [~] 38.6 Property test: every reporter renders the same finding set.
    - Property 18. Validates Requirements 2.2, 2.5, 2.8, 2.9, 2.12, 7.6, 23.4, 23.5, 24.1 to 24.5. The code string is byte-identical across the text line, the ignore entry, the configuration key and the SARIF rule identifier.

- [ ] 39. Determinism, the API-surface record and the conformance run
  - [~] 39.1 Implement the determinism guarantees: no cache, no time budget, no truncation, no sharding.
    - Verify: two consecutive runs produce byte-identical bytes; an empty cache directory matches a populated one. _Requirements: 26.1 to 26.3, 34.4. Design: Reporters and performance._
  - [~] 39.2 Write `api-surface.json` listing every import specifier and named import the analyzer consumes, `SymbolFlags` under `typescript/unstable/sync`, with a test asserting the actual import set equals it.
    - Verify: the test fails when an import is added or removed without updating the record, and when `SymbolFlags` is imported from `unstable/ast`; the record states that the factory, visitor, proto, fs and async paths are not consumed. _Requirements: 29.12. Design: Consumed API paths, and the drift watch._
  - [~] 39.3 Add the scheduled run of the fixture set against the pinned `typescript` version and against the `next` distribution tag.
    - Verify: the schedule runs both and asserts the `next` tag's export map equals the pinned 7.0.2 map; a simulated export-path rename fails the `next` run naming the moved symbol. _Requirements: 29.12, 34.20. Design: Consumed API paths, and the drift watch._
  - [~] 39.4 Run THE Conformance_Corpus inside the TypeScript test suite and commit `conformance.json` naming the corpus version and every declined capability.
    - Verify: the suite passes the corpus; the result and its digest appear in `analyzer.conformance` in every report. _Requirements: 2.16 to 2.18, 30.14, 34.14, 34.15. Design: The Conformance Corpus._
  - [~] 39.5 Property test: a run is byte-identical to its own repetition, and independent of any cache.
    - Property 22. Validates Requirements 26.1, 26.3, 34.4. The generator produces target trees and cache states.

- [ ] 40. The TypeScript intra-function group
  - [~] 40.1 Emit the five intra-function kinds TypeScript carries, `DS1801`, `DS1803`, `DS1805`, `DS1807` and `DS1809`, over the AST and the batched checker, lifting `tsc`'s `noUnusedParameters` and `noUnusedLocals` rules and the `allowUnreachableCode` diagnostic without setting those compiler options, with `unparam`'s exemptions applied by name to `DS1801` and `DS1803` (an exported function in a project whose consumer set is not declared complete, a method satisfying an interface, a function used as a value, a stub) and `DS1803` reporting only when every call site is in the loaded graph, each message naming its overlapping rule from `kinds.json`, with the same suppression, confidence, reporter and exit-code treatment as the rest.
    - Verify: one firing and one non-firing fixture per code, the non-firing `DS1801` cases being a method whose class is retained by satisfaction, a callback passed as a value and an exported function with `consumers.complete` unset; a project that does not set `noUnusedParameters` still reports `DS1801` and does not exit 3; a kind whose overlap row reads `none known` says so rather than omitting the field. _Requirements: 20.2, 20.4 to 20.7, 29.2, 29.3. Design: The intra-function group._
  - [~] 40.2 Wire the group's one enable switch to the `DS18` range prefix, enabled by default.
    - Verify: a fresh configuration with no severity section reports all five kinds; disabling one by code leaves the other four reported; the `DS1802` corpus fixture carries a Go rendering only, so the TypeScript run answers no expectation for it and records no gap. _Requirements: 2.7, 20.1, 20.3. Design: The intra-function group._

- [ ] 41. Documentation for a reader outside this fleet
  - [~] 41.1 Write the TypeScript analyzer's reference documentation: every kind, every exemption class, the reachability classes, the reference-versus-reachability difference with its complementary tool, the TypeScript 7 baseline and the removed compiler API, the Contract version, the declared gaps, the platform set and the non-goals.
    - Verify: the documentation names no host, workflow or internal vocabulary of this fleet, and every exported symbol carries a doc comment. _Requirements: 1.13, 5.6, 23.8, 29.4, 35.1 to 35.7, 35.9 to 35.11. Design: The TypeScript analyzer._
  - [~] 41.2 Write the documentation coverage test over the implemented kinds, classes and declared gaps.
    - Verify: the test fails when a kind is implemented with no documentation entry. _Requirements: 35.1, 35.2, 35.6. Design: Testing Strategy._

- [~] 42. Checkpoint. `deadset-ts` is complete and conformant
  - Ensure all tests pass, ask the user if questions arise. Confirm the batch-cap calibration record and the API-surface record before the measurement run, because both are what keep an upstream change from arriving as a mystery.

### `deadset`: the orchestrator

- [ ] 43. The edge proven first, then configuration, language detection and the provider list
  - [~] 43.1 Prove the cross-language edge end to end before any other orchestrator work: build a harness holding one `wiregen` edge between a real Go server type and its generated TypeScript client type, declared in `deadset-edges.json`, run `deadset-go analyze` and `deadset-ts analyze` directly to obtain two real reports, and merge them once through the merge of tasks 45 and 46. This leaf gates 43.4, 44.1, 48 and 49 by settled decision 33; if its criterion cannot be met, the orchestrator is what is dropped before the first release.
    - Verify: the go/no-go criterion, recorded as a committed harness result: with the TypeScript side live the Go type is not reported; with the TypeScript side dead the Go type is reported and both symbols share one component; `deadset-go analyze` run alone over the harness exits 4 with the pending count named; the same three outcomes hold for a `DS1101` candidate whose only outside reference is the edge. _Requirements: 8.7, 25.8, 31.4 to 31.7, 31.11, 34.18, 34.19. Design: The cross-language edge, and the pending finding, The merge, The full first release with the orchestrator, rather than a vertical slice._
  - [~] 43.2 Implement configuration decoding with `encoding/json` under `DisallowUnknownFields` against the closed key list, `print-config` with its `provenance` object, the target-kind requirement, the unimplemented-key refusal, and the split that hands each analyzer the section it owns.
    - Verify: every published configuration vector passes; a language in scope with no configuration supplying the target kind exits 2 naming the field rather than skipping. _Requirements: 27.1 to 27.18, 33.4. Design: Configuration._
  - [~] 43.3 Implement language detection by marker file and extension over one directory walk, honoring the path filters and the ignore directories, with the configured language set as an override.
    - Verify: a Go-only tree runs one analyzer; a mixed tree runs two; a tree whose only Go text sits inside a string literal is not detected as Go; the configured override wins. _Requirements: 30.5, 30.6, 33.1. Design: Language detection, Detection by marker and extension rather than by content._
  - [~] 43.4 Implement the provider list: the two default entries, add, edit and delete including deleting a default entry, and more than one entry claiming one language. Runs after 43.1 has passed.
    - Verify: a list with two Go analyzers runs both; a deleted default entry is not run; each entry's name, languages, source, version and digest are read. _Requirements: 30.1 to 30.4. Design: The provider list._
  - [~] 43.5 Property test: the orchestrator is never a required hop.
    - Property 29. Validates Requirements 30.4, 30.19. For a single-language target the orchestrator's findings equal a direct analyzer invocation, and each matching provider entry runs exactly once. Needs one finished analyzer.

- [ ] 44. The describe handshake and process invocation
  - [~] 44.1 Implement the `describe` handshake: the conformance-pass check, the accepted schema-version range and the absent-command check, each with its own exit-3 message. Runs after 43.1 has passed, because it reads provider entries.
    - Verify: an analyzer with no conformance pass exits 3 naming it; a schema version outside the range exits 3 naming both versions; a provider entry naming an absent command exits 3 naming the analyzer and the entry. _Requirements: 30.9, 30.14, 30.15, 30.20. Design: Invocation, and the handshake before it._
  - [~] 44.2 Implement the run directory: the scope document with declared roles, the per-analyzer configuration section, and the retention of every input report as the run's evidence.
    - Verify: a run leaves `describe.json`, `scope.json`, `config.<lang>.json` and `report.<lang>.json` per analyzer plus the merged report; the scope names only local filesystem paths. _Requirements: 12.4, 12.12, 27.18, 30.7. Design: The analyzer CLI contract, A report file rather than stdout._
  - [~] 44.3 Implement `analyze` invocation as a separate process with `--report=PATH`, and the fail-closed reads: an analyzer exiting 3, an absent report, and a report that fails to decode into the typed report structs under `DisallowUnknownFields`; no JSON Schema validator runs at run time.
    - Verify: no analyzer is linked as a library, asserted by the import graph; an analyzer exiting 3 makes the run exit 3 and presents no other analyzer's findings; a truncated report and a report carrying an undeclared field each exit 3 naming the analyzer, the path and the decode error; the import graph holds no schema-validation module. _Requirements: 30.7, 30.8, 30.18. Design: A report file rather than stdout, Invocation, and the handshake before it, Error Handling._

- [ ] 45. The merge
  - [~] 45.1 Implement admission and union: the schema-version and conformance checks per input, then every finding, declared gap and stale-suppression record carried through with no deduplication.
    - Verify: two analyzers claiming one language double every shared finding, ordered stably; no input record is dropped. _Requirements: 30.15 to 30.17. Design: The merge._
  - [~] 45.2 Implement the canonical order over the merged envelope, with the stated tiebreak chain ending in the analyzer name.
    - Verify: a mixed-language report interleaves by path rather than grouping by language; two identical findings from two analyzers order stably. _Requirements: 2.13, 30.17. Design: The merge._
  - [~] 45.3 Run the published merge vectors byte-exactly in the orchestrator's own suite.
    - Verify: every vector's merged bytes and exit code match; a planted one-field change fails. _Requirements: 2.14, 30.16, 34.17. Design: Merge test vectors._
  - [~] 45.4 Property test: the merge preserves every input record and is independent of input order.
    - Property 26. Validates Requirements 2.13, 30.16, 30.17. The generator presents report sets in arbitrary orders and compares merged bytes.

- [ ] 46. Pending resolution and stale edges
  - [~] 46.1 Implement the edge index from every report's `edge_evaluations` and the resolution of every evaluation carrying a finding: drop when a paired side is live, promote when every other side is dead.
    - Verify: the live-pair and dead-pair vectors pass; the merged report holds no evaluation carrying a finding. _Requirements: 31.6, 31.10, 31.11. Design: The merge._
  - [~] 46.2 Implement component unioning in canonical order, so a promoted pending finding shares one component with its paired symbols and the identifiers do not depend on which analyzer finished first.
    - Verify: two runs with the analyzers' completion order swapped produce identical component identifiers. _Requirements: 31.7. Design: The merge._
  - [~] 46.3 Implement `DS1705` for an edge every side reports absent, and exit 3 for a pending finding whose edge appears in no other report.
    - Verify: the two corresponding vectors pass; the exit-3 message names the unresolved edge, the pending side and the reports searched. _Requirements: 31.8, 31.9. Design: The merge, Error Handling._
  - [~] 46.4 Property test: the merge resolves every pending finding by its pair's state and emits none.
    - Property 27. Validates Requirements 31.4 to 31.7, 31.10, 34.18. The generator produces edge sets and pair states in every combination.

- [ ] 47. The verdict, the reporting and the continuous-integration contract
  - [~] 47.1 Implement the exit-code table over the merged report.
    - Verify: one run per code from 0 to 4 over vector-derived inputs; a warn-only report exits 0; a stale suppression exits 1 whatever the rest of the severity map holds. _Requirements: 25.1 to 25.9, 30.16. Design: The exit-code table._
  - [~] 47.2 Implement the one summary line covering every language it ran, and the annotation reporter over the merged set.
    - Verify: the summary prints the finding count, the suppression count, the stale-suppression count and the recorded-reason count; one error annotation per failing finding and per stale suppression; the output names each analyzer's version and digest. _Requirements: 24.3, 30.20, 33.5, 33.7, 33.8. Design: The orchestrator._
  - [~] 47.3 Implement the remediation text and `explain`, including the edge identifier and the paired symbol's state in the other report.
    - Verify: a failing run prints the three remediation choices; an explanation for a symbol carrying an edge names the edge and the pair's state. _Requirements: 22.6, 33.6. Design: Components and Interfaces._
  - [~] 47.4 Assert determinism and report-only behavior over a real end-to-end run.
    - Verify: two consecutive runs produce byte-identical merged bytes; every file of the target is unchanged; a request for a source edit exits 2. Needs one finished analyzer. _Requirements: 23.1, 23.6, 23.7, 26.1, 34.4, 34.11. Design: Error Handling._

- [ ] 48. `install` and the analyzer cache, after 43.1 has passed
  - [~] 48.1 Implement the `install` verb: fetch at the pinned version, verify against the recorded digest before the artifact is ever executed, and refuse on mismatch.
    - Verify: a digest mismatch exits 3 naming the artifact and both digests, and the artifact is not executed, asserted by a process-spawn counter; acquisition never runs during an analysis. _Requirements: 30.8, 30.10, 30.11. Design: Acquisition, off by default._
  - [~] 48.2 Implement the cache layout with the digest in the path and the `current` link, so a later run finds the analyzer present and performs no acquisition.
    - Verify: a changed digest creates a new directory rather than overwriting; a second run makes no network request; the cache holds executables and never analysis results. _Requirements: 26.3, 30.12, 30.13. Design: Acquisition, off by default._

- [ ] 49. Packaging and the two supported ways to run, after 43.1 has passed
  - [~] 49.1 Build `deadset` as one static binary with cgo disabled, installable by the Go toolchain at a version.
    - Verify: `go install` at a tag produces a binary that runs on a Linux host with no C toolchain and no Go toolchain present. _Requirements: 32.1, 32.8. Design: Distribution and operations._
  - [~] 49.2 Build the container image: a Go stage for the two binaries, a Node stage for the TypeScript analyzer at its exact `typescript` pin, a Debian runtime carrying both plus Node, and `/usr/share/deadset/manifest.json` recording every version and digest.
    - Verify: the image analyzes a mixed fixture given an explicit scope and makes no network request; the manifest lists every product and runtime with its digest. _Requirements: 32.2, 32.7, 32.12. Design: Distribution and operations._
  - [~] 49.3 Publish the GitHub Action: its own pinned `deadset` version, the analyzer install as a setup step before the analyzing step, the five inputs and the five outputs.
    - Verify: a workflow using the action on an Ubuntu runner with no container analyzes a mixed repository, performs no acquisition during the analysis step, and exposes the finding, stale-suppression, deletable-line, SARIF-path and report-path outputs. _Requirements: 12.12, 30.13, 32.3 to 32.6. Design: Distribution and operations._

- [ ] 50. Documentation for a reader outside this fleet
  - [~] 50.1 Write the orchestrator's reference documentation: the cross-language edge, the pending state and why neither analyzer resolves an edge alone; that each analyzer is usable on its own; the provider list and what the orchestrator does and does not vet; the platform set; every non-goal with its reason and its alternative tool.
    - Verify: the documentation names no host, workflow or internal vocabulary of this fleet, and every exported symbol carries a doc comment. _Requirements: 1.13, 35.5 to 35.11. Design: The orchestrator, Non-goals._
  - [~] 50.2 Write the documentation coverage test over the declared gaps, the Contract version and the non-goal list.
    - Verify: the test fails when the Contract version in the documentation and in the resolved configuration disagree. _Requirements: 27.17, 35.6. Design: Testing Strategy._

- [~] 51. Checkpoint. One command answers a mixed repository
  - Ensure all tests pass, ask the user if questions arise. The merge vectors are green against synthetic reports; from here the checks need real analyzer output.

### Cross-product work

- [ ] 52. Cross-language agreement over both analyzers
  - [~] 52.1 Run the agreement checker over both analyzers' real `conformance-results.json` files and resolve every disagreement.
    - Verify: the checker reports zero disagreements, or each remaining one is a recorded declared gap on one side; a planted divergence still fails. Property 33's harness is exercised here against real inputs. _Requirements: 2.19, 34.16. Design: Cross-language agreement._
  - [~] 52.2 Run the two-phase suppression agreement fixture in both languages: analyze, write the reported finding into `deadset-ignore.json` in the documented form, analyze again.
    - Verify: both languages suppress exactly that finding, report no stale entry, and agree on the matching and scoping decision. _Requirements: 21.12, 34.8, 34.16. Design: Cross-language agreement._
  - [~] 52.3 Close every declined capability or record it as a declared gap in both `conformance.json` files, and print the gap table in each product's conformance result.
    - Verify: no expectation is silently omitted by either product; the corpus fails a silent omission. _Requirements: 2.17, 2.18, 34.15. Design: Declared gaps._

- [ ] 53. The end-to-end mixed-repository run
  - [~] 53.1 Promote the harness of task 43.1 into the mixed fixture repository: a Go type paired with generated TypeScript by a declared edge, run end to end through the shipped orchestrator command rather than through direct analyzer invocations, in the live-pair and dead-pair shapes.
    - Verify: the live pair produces no finding for the pending symbol; the dead pair reports both symbols in one dead component; the result equals the harness result recorded at 43.1. _Requirements: 31.6, 31.7, 34.18. Design: The cross-language edge, and the pending finding._
  - [~] 53.2 Assert the single-analyzer pending behavior at orchestrator level: one analyzer alone over that fixture, and the orchestrator's refusal to leave a pending finding in a merged report.
    - Verify: the direct analyzer run exits 4; the orchestrator run over the same fixture exits from the table with an empty `pending` array. _Requirements: 25.8, 31.10, 34.19. Design: The merge._
  - [~] 53.3 Run the orchestrator over the fleet's real mixed repository, the one whose Go wire types are consumed by generated TypeScript, with the edges declared by the generator that created the pairs.
    - Verify: a recorded run naming the finding count per language, the resolved pending count and the exit code; a stale edge in that repository reports `DS1705`. _Requirements: 31.1, 31.6, 31.9, 33.1, 33.8. Design: The cross-language edge, and the pending finding._

- [ ] 54. Migration guidance published by the tools themselves
  - [~] 54.1 Write the manual migration guide: the per-repository pass, the exemption classes that retire each adjudication class, the counts the design predicts, and the plain statement that no converter and no compatibility mode exists.
    - Verify: the guide names, per census class, the exemption class or mechanism that retires it and the count, and states the six breaking-change entries as the predicted residue. _Requirements: 21.16, 21.17, 23.8. Design: Migration._
  - [~] 54.2 Write the migration note carrying the `EU1001` and `EU1002` mapping into the code space.
    - Verify: the mapping lives in the migration notes and not in the code space, asserted by the vocabulary self-test. _Requirements: 2.6. Design: The code space._
  - [~] 54.3 Write the enrolment procedure for a TypeScript package: the `deadset.json` a package needs, the target kind it declares, where the reasons a `knip.jsonc` carried as comments go, and why a package whose configuration is removed fails rather than passing quietly.
    - Verify: the procedure applied to one package that carries a `knip.jsonc` today produces a passing configured run with every comment reason moved into an entry's `reason` field, and the same package with the configuration removed exits 2. _Requirements: 27.14, 33.3, 33.4. Design: Step 4: the TypeScript side, replacing ten configurations._
  - [ ]* 54.4 Ship a differential harness that compares a `deadset` report against an existing `.punused-ignore` or `knip` suppression set, for use during the per-repository passes.
    - Verify: the harness prints the entries an exemption retired, the entries still flagged and the entries it cannot classify. No requirement obliges this leaf; it exists to make the follow-up passes measurable.

### The release gates

- [ ] 55. The `deadset-go` measurement run
  - [~] 55.1 Build the measurement harness and the per-repository record format under `measurement/<repo>.json`, carrying the finding count, the suppression count, the entry count, the classification of every entry into exactly one reason class, the resolved adjudications with the class that resolved each, and the entries still flagged.
    - Verify: the harness produces a record for one repository whose numbers match a hand-checked run and whose per-class counts sum to its entry count; the record lives in the analyzer's own repository so a later release compares against it. _Requirements: 36.3 to 36.6, 36.10. Design: The measurement run._
  - [~] 55.2 Run the harness across every repository in this fleet holding a Go module, with the 257 existing `.punused-ignore` entries as the oracle, each classified into exactly one reason class and resolved either as a retained symbol or as a reported finding.
    - Verify: the records' entry counts sum to 257 across 18 files; every entry is classified once and resolved one way or the other, with no unclassified entry left; the run makes no network request and edits no file. _Requirements: 36.1, 36.4, 23.6. Design: The measurement run._
  - [~] 55.3 Check the run against the design's prediction and publish the comparison, per reason class: the entries classified into the class, the entries resolved with no suppression present by the class's exemption, the entries resolved by loading a consumer, the entries reclassified as a test-only finding, and the residue that stays a suppression.
    - Verify: the published table gives, per class, the classified count and the resolved count with the two summing to 257 over all classes, names every shortfall as an exemption class still to build rather than as a judgement, and states that the keyword census of the research documents counted comment lines and is not the number compared; the removal-would-be-breaking entries appear as the residue or the difference is explained. _Requirements: 21.17, 36.6, 36.7. Design: Migration, Step 3: the adjudications, The measurement run._
  - [~] 55.4 Add a fixture pinning every finding a maintainer judges a false positive during the run.
    - Verify: each new fixture fires before the fix and does not fire after it, with the red check recorded. _Requirements: 34.13, 36.9. Design: Testing Strategy._
  - [~] 55.5 Record the maintainer's review of the run as the release condition.
    - Verify: the committed review names the run, its date, the analyzer version and the decision; no release tag exists before that record. _Requirements: 34.22, 36.8. Design: The measurement run._

- [ ] 56. The `deadset-ts` measurement run
  - [~] 56.1 Enrol all 11 TypeScript packages in this fleet, each with a `deadset.json` declaring its target kind, replacing the ten `knip` configurations (six `knip.jsonc`, four `knip.json`) that enrol them today.
    - Verify: every one of the 11 runs and reports; a package with its configuration removed exits 2 rather than passing; every comment reason from a `knip.jsonc` has a `reason` field to land in. _Requirements: 27.14, 33.3, 33.4, 36.2. Design: Step 4: the TypeScript side, replacing ten configurations._
  - [~] 56.2 Run the measurement harness across all 11 packages with the existing suppression entries as the oracle.
    - Verify: every one of the 41 entries is classified once and resolved as a retained symbol, a reported finding or a stale entry; every package has a finding count from `deadset-ts` for the first time. _Requirements: 36.2 to 36.4. Design: The measurement run._
  - [~] 56.3 Check the run against the design's prediction for the TypeScript side: the entries deleted rather than migrated, the bare-name entries that cannot migrate at all, and the number of sites each becomes.
    - Verify: the published table names the entries matching nothing today, the entry naming a file that no longer exists, and the site count for each bare-name entry, so the migration cost is a number rather than an estimate. _Requirements: 21.7, 21.16, 36.6, 36.7. Design: Step 4: the TypeScript side._
  - [~] 56.4 Add a fixture pinning every finding a maintainer judges a false positive, including the private class member the fleet's mutation run found and `knip` could not see.
    - Verify: each new fixture fires before the fix and does not fire after it, with the red check recorded; the private-member case is in the corpus rather than only in the analyzer's own suite. _Requirements: 34.13, 36.9. Design: Members, which is the capability nothing else has._
  - [~] 56.5 Record the maintainer's review of the run as the release condition.
    - Verify: the committed review names the run, its date, the analyzer version and the decision; no release tag exists before that record. _Requirements: 34.22, 36.8. Design: The measurement run._

- [ ] 57. First releases
  - [~] 57.1 Release `deadset-spec` at the Contract and corpus versions every product names.
    - Verify: each product's report and resolved configuration name the released Contract version, and each product's `conformance.json` names the released corpus version. _Requirements: 2.20, 27.17, 32.14. Design: The Contract._
  - [~] 57.2 Release `deadset-go` and `deadset-ts`, each at one version across every channel it publishes to.
    - Verify: `go install` at the tag and the npm and JSR packages at the same tag all resolve; each release passes the corpus and carries its recorded measurement review. _Requirements: 1.7, 2.16, 32.9, 32.10, 32.14, 34.22. Design: Distribution and operations._
  - [~] 57.3 Release `deadset` with the container image and the GitHub Action, each pinning the analyzer versions it ships.
    - Verify: the image manifest and the Action's pinned version name the released analyzer versions and digests; a workflow using the Action at the tag analyzes a mixed repository green. _Requirements: 30.13, 32.2, 32.5, 32.12, 32.14. Design: Distribution and operations._

- [~] 58. Final checkpoint
  - Ensure all tests pass, ask the user if questions arise. Confirm that both measurement reviews are recorded, that the corpus reports zero disagreements, and that nothing in this fleet's shared workflows has been touched, because that is deliberately the next piece of work rather than part of this one.

## Notes

- Task numbers 9, 15, 28, 42, 51 and 58 are checkpoints. They carry no sub-tasks and no code, and they exist where the next phase would otherwise build on an unverified layer.
- A leaf sub-task is the unit of work. A parent task is a grouping and is finished when its leaves are.
- Test leaves carry `*` only where no requirement obliges them, which is why there are two: 25.6 and 54.4. Requirement 34 makes the fixture pairs, the golden comparisons, the red-check demonstrations, the property tests and the corpus run conditions of a release, and Requirement 36 makes the measurement run one, so marking any of those optional would let an executing agent skip a release gate.
- Each of the design's 33 correctness properties is implemented once, in the product whose code the design spells the mechanism out for. `deadset-go` carries properties 1, 3, 4, 5, 6, 7, 10, 13, 14, 17, 19, 20, 21, 23, 24, 25 and 30; `deadset-ts` carries 2, 8, 9, 11, 12, 15, 16, 18, 22, 28, 31 and 32; `deadset` carries 26, 27 and 29; `deadset-spec` carries 33. Where the same behavior exists in the other product, the corpus expectation (Requirement 2.19) and the published vectors (Requirements 2.14, 27.15) are what bind it, which is the design's own argument for why two independent implementations agree.
- The red check is part of every detection leaf rather than a late task, because a check whose assertion still passes with the check reverted is testing nothing and the cheapest moment to find that out is the moment the check is written.
- A corpus fixture ships with the kind it pins. No task collects fixtures afterwards, because a kind that entered an analyzer without its expectation is a kind the other analyzer is free to disagree about.
- No task builds, fixtures or tests a kind settled decisions 27 and 32 removed. The nineteen retired codes appear in exactly two places in this plan: the retired rows of `kinds.json` (task 2.2) and the vocabulary self-test that refuses their re-use (task 2.4). A task that reintroduces one is a task the Contract's own suite fails.
- No task builds a parser. Every document a product reads or writes is JSON decoded by the standard library (settled decision 29), so the configuration leaves (10.2, 29.3, 43.2) are decoders against a closed key list, and the published configuration vectors are what keep the two implementations in agreement.
- The edge is proven first. Task 43.1 runs before the provider list (43.4), the handshake (44.1), acquisition (48) and packaging (49), and its committed harness result is the go/no-go record settled decision 33 names; the merge (45, 46) is built earlier against the published vectors so that 43.1 has a merge to call.
- No task in this plan edits this fleet's shared workflows, deletes a `.punused-ignore` file or rewrites an existing adjudication. That is the follow-up work below.

## Follow-up work outside these four repositories

Named here rather than as tasks, because each one lands in a repository this plan does not create.

- **The shared continuous-integration workflows.** Adding a `deadset` step beside the existing `punused` step with every kind at `warn`, soaking one cycle, comparing the two finding sets per repository, then promoting the severities, deleting the `punused` step and its language-server pin, and deleting the `knip` step and its configuration files. Two changes rather than one, because replacing a measured gate with an unmeasured one in a single commit is how a regression ships. Requirements 33.2 and 33.3 are what the replacement satisfies; the design's Migration section carries the ordering. The same change decides, per intra-function kind, which side of the overlap the fleet keeps: `deadset`'s `DS18xx` rows or the `unparam`, `revive`, `ineffassign`, `staticcheck` and `eslint` rules the shared linter configurations already enable, so that the six kinds now on by default do not arrive twice. One consequence of settled decision 27 lands here too: deleting the `knip` step also deletes its `unlisted` and `unresolved` dependency rules, which `deadset-ts` deliberately does not carry, and on the TypeScript side `tsc` resolves an undeclared package from `node_modules` without complaint, so the workflow change either keeps a dependency-only `knip` configuration for those two rules or records that the fleet accepts the gap.
- **The per-repository adjudication passes.** One pass per repository: run with every kind at `warn`, diff the finding set against the existing entries, delete every entry an exemption class retired, and hand-write a `deadset-ignore.json` entry for each survivor with its reason carried over from its comment into the entry's `reason` field. Task 54.4 exists to make each pass measurable rather than judged.
- **The bare-name suppression entries on the TypeScript side.** The two entries covering 60 and 23 files cannot migrate, because Requirement 21.7 refuses a bare name. Each becomes either an exemption the analyzer computes or one scoped entry per site, and task 56.3 reports the site count that decides which.
- **The release-configuration asymmetry that the deleted `knip` configuration leaves behind.** With the file gone, the exclusion that let a `refactor:` commit touching only that configuration bump a version has nothing to act on, so the exclusion goes too.
- **Replacing the ten `knip` configurations in the gate.** Task 56.1 gives every one of the eleven TypeScript packages a `deadset.json` for the measurement run; swapping the gate's `knip` step for the `deadset` step in front of them is a change to the workflows.

## Deferred to a later release

- **Fix mode.** No product edits a source file, and none ships a mode that does (Requirement 23.1). The deletion set, the narrowing set, the component ordering and the fixability classification this plan builds are exactly the inputs such a mode would need, and the constraints already recorded for it are that it never edits a generated file, never edits a file outside the target, and never acts on a finding whose answer lies in another report.
- **A converter for another tool's suppression format.** Refused outright by Requirement 21.16 rather than deferred; migration stays a manual change made once per repository.
- **Runtime liveness as an input.** Coverage profiles, production usage logs and tombstones stay outside the analysis. A coverage profile may later be accepted as a confidence input, and the analysis will still not depend on one.
- **Inferring a cross-language edge.** An edge is declared by a maintainer or written out by the generator that created the pair. Inferring one needs an analyzer to read the other language, which is the thing the Introduction rules out.
- **Platforms other than Linux**, and **TypeScript 6 support**. Both are non-goals rather than backlog items.
- **None of the nineteen kinds settled decisions 27 and 32 removed.** Each is owned by a tool this fleet's shared workflows already run (the compiler, `knip`, `unparam`, `unconvert`, `go vet`, `staticcheck`, `eslint`) or is detectable only heuristically, the two newest (`DS1607` unused-workspace-entry and `DS1608` unused-binary) because a `go.work` `use` entry and a manifest `bin` are external entry points by definition; so none is deferred: they are recorded in the requirements' Non-goals, in the steering document's tiers, and nowhere in this plan.

## Task Dependency Graph

```json
{
  "waves": [
    { "id": 0, "tasks": ["1.1", "1.2", "1.3", "1.4"] },
    { "id": 1, "tasks": ["2.1", "2.2", "2.3", "4.1", "4.2", "4.3", "5.1", "6.1", "7.1", "7.2"] },
    { "id": 2, "tasks": ["2.4", "3.1", "3.2", "5.2", "7.3", "8.1"] },
    { "id": 3, "tasks": ["3.3", "4.4", "5.3", "6.2", "7.4", "8.2"] },
    { "id": 4, "tasks": ["6.3", "7.5"] },
    { "id": 5, "tasks": ["10.1", "10.2", "29.1", "29.2", "29.3", "43.2", "43.3"] },
    { "id": 6, "tasks": ["10.3", "10.4", "11.1", "29.4", "30.1", "44.2"] },
    { "id": 7, "tasks": ["10.5", "10.6", "10.7", "10.8", "11.2", "30.2", "44.3", "45.1"] },
    { "id": 8, "tasks": ["11.3", "11.4", "12.1", "30.3", "31.1", "45.2"] },
    { "id": 9, "tasks": ["12.2", "12.3", "30.4", "30.5", "31.2", "45.3", "45.4"] },
    { "id": 10, "tasks": ["13.1", "13.2", "13.3", "31.3", "32.1", "46.1"] },
    { "id": 11, "tasks": ["14.1", "14.2", "31.4", "31.5", "31.6", "32.2", "46.2"] },
    { "id": 12, "tasks": ["14.3", "14.4", "14.5", "33.1", "33.2", "46.3", "46.4"] },
    { "id": 13, "tasks": ["16.1", "16.2", "33.3", "34.1", "34.2", "47.1", "47.2"] },
    { "id": 14, "tasks": ["16.3", "16.4", "33.4", "34.3", "34.4", "34.5", "47.3"] },
    { "id": 15, "tasks": ["16.5", "16.6", "17.1", "17.2", "35.1", "35.2"] },
    { "id": 16, "tasks": ["16.7", "16.8", "17.3", "18.1", "18.2", "35.3"] },
    { "id": 17, "tasks": ["18.3", "18.4", "35.4", "35.5", "36.1"] },
    { "id": 18, "tasks": ["18.5", "18.6", "19.1", "19.2", "36.2", "36.3"] },
    { "id": 19, "tasks": ["19.3", "19.4", "20.1", "36.4", "36.5"] },
    { "id": 20, "tasks": ["19.5", "20.2", "20.3", "21.1", "36.6", "50.1"] },
    { "id": 21, "tasks": ["21.2", "21.3", "22.1", "36.7", "50.2"] },
    { "id": 22, "tasks": ["22.2", "22.3", "36.8", "37.1"] },
    { "id": 23, "tasks": ["23.1", "23.2", "23.3", "37.2", "37.3"] },
    { "id": 24, "tasks": ["23.4", "23.5", "24.1", "37.4", "38.1"] },
    { "id": 25, "tasks": ["23.6", "24.2", "24.3", "37.5", "37.6", "38.2", "43.1"] },
    { "id": 26, "tasks": ["24.4", "24.5", "24.6", "38.3", "38.4", "38.5", "43.4", "48.1"] },
    { "id": 27, "tasks": ["24.7", "24.8", "24.9", "24.10", "38.6", "39.1", "44.1", "48.2"] },
    { "id": 28, "tasks": ["25.1", "25.2", "26.1", "26.2", "39.2", "39.3", "40.1", "49.1"] },
    { "id": 29, "tasks": ["25.3", "25.4", "25.5", "25.6", "26.3", "27.1", "39.4", "39.5", "40.2", "41.1", "49.2"] },
    { "id": 30, "tasks": ["27.2", "41.2", "43.5", "47.4", "49.3", "52.1", "52.2", "53.1", "53.2"] },
    { "id": 31, "tasks": ["52.3", "53.3", "54.1", "54.2", "54.3", "54.4", "55.1", "56.1"] },
    { "id": 32, "tasks": ["55.2", "56.2"] },
    { "id": 33, "tasks": ["55.3", "55.4", "56.3", "56.4"] },
    { "id": 34, "tasks": ["55.5", "56.5"] },
    { "id": 35, "tasks": ["57.1", "57.2", "57.3"] }
  ]
}
```
