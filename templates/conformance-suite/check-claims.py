#!/usr/bin/env python3
"""The claims ledger: every 'what changed' cell in a round's disposition names
an operation, refusal, mutant or invariant that exists in the suite.

  check-claims.py docs/reviews/round-N/disposition.md verify-operations.sh [sql/schemas.sql]

A claim is a backticked name or a quoted phrase in the 'What changed' column;
each must appear in the suite as an op/refuse/mutant title or an invariant
message. Exit 1 and list the unbacked claims otherwise. 'stated' / 'covered'
with nothing behind them are exactly what this catches."""
import re, sys
disp, suite = open(sys.argv[1]).read(), open(sys.argv[2]).read()
schema = open(sys.argv[3]).read() if len(sys.argv) > 3 else ''
corpus = suite + schema
titles = set(re.findall(r'^(?:op|refuse|mutant) "([^"]+)"', suite, re.M))
msgs = set(m.replace("''", "'").rstrip(': ') for m in re.findall(r"^SELECT '((?:[^']|'')+?)' \|\|", suite, re.M))
fns = set(re.findall(r'CREATE FUNCTION (\w+)', suite))
missing = []
for row in re.findall(r'^\| \d+ \|.*$', disp, re.M):
    cells = [c.strip() for c in row.strip('|').split('|')]
    if len(cells) < 4: continue
    what = cells[3]
    for claim in re.findall(r'`([^`]+)`', what):
        c = claim.strip('()').split('(')[0].strip()
        if not re.match(r'^[A-Za-z_][A-Za-z0-9_.]*$', c): continue          # a phrase, an arrow, a comparison: not a name
        if c.endswith('.md') or c.endswith('.sh'): continue                 # a file
        if c in fns or any(c in t for t in titles) or any(c in m for m in msgs): continue
        leaf = c.split('.')[-1]                                             # table.column → the column is the name that must exist
        if re.search(r'\b' + re.escape(leaf) + r'\b', corpus): continue     # a DDL name, a column, a function the schema has
        missing.append(claim)
    if re.search(r'\b(stated|covered|documented)\b', what) and not re.search(r'`', what):
        missing.append("prose-only claim: " + what[:60])
print(f"{len(titles)} titles, {len(msgs)} invariant branches, {len(fns)} functions in the suite")
if missing:
    print("UNBACKED CLAIMS:"); [print("  -", m) for m in missing]; sys.exit(1)
print("every claim is backed")
