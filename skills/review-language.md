# Review Language — plain English contract

**Scope:** how every review, finding, and PR comment is written. Loaded by all reviewer seats and the PR reviewer. This file does not change WHAT gets reviewed — only how the result reads.

**Why:** the team reads English as a second language. Long sentences and idioms hide findings. A finding nobody fully understands does not get fixed.

---

## The rules

1. **Short sentences.** Aim for 15 words or fewer. One idea per sentence. If a sentence has two commas, split it.
2. **No idioms, no metaphors.** Banned examples: "free-roll", "load-bearing", "rides the release", "foot-gun", "in the weeds", "moves the needle". Say the literal thing instead.
3. **Active voice, named actor.** Write "StartHit debits the wallet", not "the wallet is debited". Write "Add a check", not "a check should be added".
4. **Imperative fixes.** Start every fix with a verb: "Add…", "Rename…", "Move…", "Delete…". Never "consider whether it might be worth…".
5. **Code over prose.** If a code snippet, table, or list can carry the point, use it. Prose explains only what the code cannot show.
6. **Plain words.** "use" not "utilize" / "leverage". "because" not "owing to the fact that". "but" not "however, it should be noted that".
7. **Numbers and names, not references.** Write the file, function, and value in place. Do not write "as mentioned above" or "the aforementioned issue".
8. **Define the term or drop it.** Domain terms that stay: idempotency, CAS (compare-and-swap), fail-closed, race. On first use in a review, add a five-word gloss in parentheses if the finding depends on it.

## The finding template

Every finding uses these three parts, in this order, each 1–2 sentences:

```
**What is wrong:** <the defect, with file:line>
**What happens:** <the concrete result — who sees what, when>
**What to do:** <imperative fix, code snippet if possible>
```

The severity icon and title come first. The `principle` field keeps its lesson, in the same plain style.

## The review summary

Every review starts with a box the reader can absorb in 30 seconds:

- **Verdict** on line one.
- Then at most 5 bullets. One bullet per blocking finding. One bullet for "everything else is non-blocking".
- Each bullet ≤ 20 words.

Detail comes after the box, never instead of it.

## Self-check before posting

Read your summary once as if English were your second language:

- Any sentence you must read twice → split it.
- Any word you learned from a novel, not a textbook → replace it.
- Any finding whose fix is not a verb phrase → rewrite the fix.
