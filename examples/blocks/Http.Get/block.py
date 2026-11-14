#!/usr/bin/env python3
"""Http.Get: fetch a URL. One JSON request on stdin, one reply on stdout."""
import json
import sys
import urllib.error
import urllib.request

req = json.load(sys.stdin)
inp = req["inputs"]
url = inp["url"]

if not (url.startswith("http://") or url.startswith("https://")):
    print("only http and https URLs are allowed", file=sys.stderr)
    sys.exit(1)

r = urllib.request.Request(url, headers={str(k): str(v) for k, v in (inp.get("headers") or {}).items()})
try:
    with urllib.request.urlopen(r, timeout=25) as resp:
        status, body = resp.status, resp.read(4_000_000).decode("utf-8", "replace")
except urllib.error.HTTPError as e:
    status, body = e.code, e.read(100_000).decode("utf-8", "replace")
except Exception as e:
    print("request failed: %s" % e, file=sys.stderr)
    sys.exit(1)

json.dump({"outputs": {"status": status, "body": body}, "note": "GET %s -> %d" % (url, status)}, sys.stdout)
