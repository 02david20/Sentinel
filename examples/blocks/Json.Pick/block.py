#!/usr/bin/env python3
"""Json.Pick: JSON -> keyed list for Diff. One JSON request on stdin, one reply on stdout."""
import json
import sys

req = json.load(sys.stdin)
inp = req["inputs"]


def fail(msg):
    print(msg, file=sys.stderr)
    sys.exit(1)


data = inp["json"]
if isinstance(data, str):
    try:
        data = json.loads(data)
    except ValueError as e:
        fail("input is not valid JSON: %s" % e)

for part in [p for p in inp["path"].split(".") if p]:
    if not isinstance(data, dict) or part not in data:
        fail("path %r: no field %r" % (inp["path"], part))
    data = data[part]
if not isinstance(data, list):
    fail("path %r does not point to a list" % inp["path"])

fields = [f.strip() for f in inp["fingerprint"].split(",") if f.strip()]
items = []
for i, row in enumerate(data):
    if not isinstance(row, dict) or inp["key"] not in row:
        fail("item %d has no field %r" % (i, inp["key"]))
    vals = [row.get(f) for f in fields]
    sig = json.dumps(vals[0] if len(vals) == 1 else vals, separators=(",", ":"), sort_keys=True)
    items.append({"id": str(row[inp["key"]]), "sig": sig})

json.dump({"outputs": {"items": items, "count": len(items)}, "note": "picked %d items" % len(items)}, sys.stdout)
