---
name: documentation
description: Turn what was learned in an agent session into a committed note — intake questions, evidence labels, the frontmatter schema, placement rules, and the checklist that has to pass before the note is committed.
---

# Documentation

Use this when work has produced knowledge that only exists in a conversation:
a run, a measurement, a decision, a bug with a cause, a design that will not be
built today. The repository's value is that every claim in it is traceable; a
claim that stays in a chat is lost.

## 1. Intake — ask before the session ends

Seven questions. Every answer becomes a sentence in the note; every gap becomes
an explicit `[U]`, never a silence.

1. **What question was being answered?** One sentence, as it would be asked by
   someone who was not in the session.
2. **What was actually run?** Command, host, config, clock, and the run id or
   artefact path. If it was not run, say so and stop calling it a result.
3. **What were the numbers?** With units and the sample they were taken from.
4. **What broke, and what was the cause?** Including the bugs you found in your
   own harness — keep them in the note.
5. **What was decided, and who decided it?** A decision without an owner is a
   rumour.
6. **What is still true tomorrow?** What the next person can rely on without
   re-running anything.
7. **What is still unknown?** State it as `[U]` with what evidence would settle
   it.

## 2. Evidence labels — on every claim, in the sentence

| Label | Means | Test |
|---|---|---|
| **[M]** | measured | a run recorded it; the artefact is named |
| **[D]** | documented upstream | a KEP, vendor doc, or source comment says it |
| **[I]** | inferred | arithmetic or reasoning from `[M]`/`[D]`; say which |
| **[U]** | unknown | nobody produced the evidence |

An inference is never phrased as a measurement. `[I]` arithmetic that is quoted
without its formula is indistinguishable from a guess, so show the formula.

## 3. Frontmatter

Every note under `research/`, `evolution/`, `benchmark/`, `blog/`:

```yaml
---
title: 'What the note answers, not what it contains'
kind: analysis | experiment | audit | design | rfc | feature | handoff | narrative | index
state: hypothesis | research | implemented | validated | benchmarked | frozen | superseded | unknown
phase: 8
date: 2026-10-30
branch: 'research/session-lifecycle-elasticity'
commits: [33c18ec, 3a4069e]
tags: [session-lifecycle, autoscaling, state-validated]
---
```

Rules that are not negotiable:

- **`date` is the commit that first introduced the file**, never the file time:
  `git log --diff-filter=A --follow --format=%ad --date=short -- <path> | tail -1`.
  The note's own `| Date |` table row is the **run** date and is allowed to differ.
- **`state` comes from evidence, not optimism.** Derived from the note's `| Status |`
  row. No status row means `state: unknown` — do not guess one on the author's
  behalf.
- **`state-*` is also a tag** (`state-hypothesis`, `state-implemented` …) so the
  published site can list every unbuilt proposal in one page.
- **`commits` only lists hashes the note itself cites.** Nothing is invented.
- **Indexes and narratives carry no `state`** — they make no claim about today.

## 4. Placement

| What you learned | Where it goes |
|---|---|
| A question, with evidence, in a series | `research/<study>/NN-topic.md` |
| What a whole era shipped and why | `evolution/NN-phase-name.md` |
| A number from a run, with its baseline commit | `benchmark/experiments/eN/RESULTS.md` |
| A contract for something that ships | `research/features/<name>.md` |
| Capacity, positioning, architecture maximum | `pitch.md` |
| Work that must happen | `ROADMAP.md` §4 open work |
| A limit that is accepted, not scheduled | `ROADMAP.md` §5 edge case |
| Narrative for an outside reader | `blog/YYYY-MM-DD-slug.md` |

A study that grows a step gets a **sub-number** (`08.1`, `08.2`), never a reused
number: two notes sharing `08` makes every "note 08" reference ambiguous and no
tool can resolve it.

## 5. Skeleton

```
| | |  status table: Status · Date · Phase · Branch · Related
## The question          what would be answered, and why it matters
## What shipped / was measured   code paths with sizes, numbers with labels
## What broke            the bugs, including your own
## Evidence             commit table: hash · date · subject
## How to check this note  the commands that reproduce the claim
## What this leaves open  the next question, and what would settle it
```

## 6. Before committing

1. Every number has a label and an artefact. Grep for naked digits.
2. `git status` — no scratch files, no build output, no secrets.
3. Link check: every relative link resolves from the note's own directory.
4. `state` matches the code: if the note says "not implemented", open the
   package and confirm; if it says "implemented", cite `file:line`.
5. `date` matches the introducing commit, not the file's mtime.
6. If the note changed a number that appears in `pitch.md`, `ROADMAP.md` or
   `pitch`'s tables, update those too — one number, one source.
7. Commit atomically, with the reason in the message: what was wrong, what is
   true now, and how it was proved.

## 7. Failures this repository has already produced

Recorded so they are not repeated:

- **A status row that outlived the code.** Two notes claimed "not implemented"
  while the packages shipped with tests. Any "not implemented" is a claim about
  the note's date, not about today.
- **A date that was really the copy time.** 57 notes had no `date:`, so the site
  rendered the build date everywhere. Missing frontmatter is not neutral.
- **An index that drifted.** Phase documents existed that no README row pointed
  at, and one study note was never listed at all.
- **A harness bug deleted instead of recorded.** The `rate = rung/20` bug in the
  swap lab is kept in the note as §5c, because the fix is only trustworthy next
  to the mistake.
- **An inference printed as a capacity claim.** GPU acceleration was expected to
  raise sessions-per-node; the A/B measured no change, because the workload never
  asked for a frame.
