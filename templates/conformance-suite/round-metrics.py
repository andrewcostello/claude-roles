#!/usr/bin/env python3
"""Append one line per review round to docs/reviews/metrics.jsonl.

  round-metrics.py docs/reviews/round-N <revision> <minutes> [suite-run.log]

Reads each seat's .md for its verdict and counts (the formats the
design-reviewer role prescribes), the disposition for accepted/rejected/deferred,
and the suite's census line for the counts. Never type a count by hand."""
import json, re, sys, os, glob
folder, rev, minutes = sys.argv[1], sys.argv[2], int(sys.argv[3])
log = sys.argv[4] if len(sys.argv) > 4 else None
def num(label, s):
    # prefer the counts table the role prescribes: | LABEL | **N** |
    m = re.search(r'^\|\s*(?:New\s+|Prior\s+)?' + label + r'\s*\|\s*\**(\d+)', s, re.I | re.M)
    if m: return int(m.group(1))
    # then a summary line: "LABEL: N" / "N LABEL" / "**LABEL:** N"
    m = re.search(r'\b' + label + r'\b\W{0,6}(\d+)\b', s, re.I) or re.search(r'(\d+)\s+(?:new\s+)?' + label + r'\b', s, re.I)
    return int(m.group(1)) if m else 0
seats = {}
for f in sorted(glob.glob(os.path.join(folder, '*.md'))):
    name = os.path.basename(f)[:-3]
    if name in ('prompt', 'prompt-cli', 'disposition'): continue
    s = open(f).read()
    v = re.search(r'Verdict\W{0,8}(APPROVE|ITERATE|ESCALATE|NOT ADOPTABLE|NOT ENTERPRISE GRADE|GAP)', s, re.I)
    seats[name] = {
        'verdict': v.group(1).upper() if v else 'NONE',
        'blocking': num('BLOCKING', s), 'major': num('MAJOR', s), 'minor': num('MINOR', s),
        'still_open': num('STILL_OPEN', s), 'regressed': num('REGRESSED', s),
        'bytes': len(s)}
d = open(os.path.join(folder, 'disposition.md')).read() if os.path.exists(os.path.join(folder, 'disposition.md')) else ''
row = {'round': int(re.search(r'round-(\d+)', folder).group(1)), 'revision': rev, 'seats': seats,
       'accepted': len(re.findall(r'^\| \d+ \|', d.split('## Rejected')[0], re.M)) if d else None,
       'rejected': len(re.findall(r'^\| R\d+ \|', d, re.M)) if d else None,
       'deferred': len(re.findall(r'^\| X\d+ \|', d, re.M)) if d else None,
       'minutes': minutes}
if log:
    c = re.search(r'(\d+) invariant branches, (\d+) mutants(?:, (\d+) REGISTRY refusals)?', open(log).read())
    t = re.search(r'(\d+) operations work, (\d+) do NOT', open(log).read())
    row['suite'] = {'operations': int(t.group(1)) if t else None, 'failing': int(t.group(2)) if t else None,
                    'invariants': int(c.group(1)) if c else None, 'mutants': int(c.group(2)) if c else None,
                    'registry_refusals': int(c.group(3)) if c and c.group(3) else None}
out = os.path.join(os.path.dirname(folder.rstrip('/')), 'metrics.jsonl')
open(out, 'a').write(json.dumps(row) + '\n'); print(json.dumps(row))
