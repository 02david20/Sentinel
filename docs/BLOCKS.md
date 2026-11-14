# Writing blocks

Sentinel's steps are **blocks**. Built-in blocks are Go code compiled into the binary. You can
also add blocks **without recompiling**: a block is a folder with a manifest and an executable
(any language). Sentinel runs it as a child process, one JSON request in, one JSON reply out.

An agent can write such a block when no built-in one fits. Nothing it writes runs in a scheduled
workflow until a **person approves the exact contents**.

```
agent writes block ──► sentinel block add/new ──► _pending/   (cannot run)
                              │
                 sentinel block check           (manifest + tests)
                 sentinel block show            (a human reads the code)
                 sentinel block approve         (a human, at a terminal)  ──► approved, hash pinned
                              │
        any later edit to the folder ──► "modified": stops loading until approved again
```

## The folder

```
Http.Get/
  block.yaml     manifest (below)
  block.py       the program: python3, sh, a compiled binary, anything executable
```

The folder name must equal the block name (`Http.Get`). Names look like `Group.Action`.

### block.yaml

```yaml
name: Http.Get
doc: >-
  Fetches a URL with GET and returns the status and body.   # required: one or two sentences
effect: read              # read | write-internal | write-external
inputs:                   # name: type. Types: string number bool list map any. "?" = optional
  url: string
  headers: map?
outputs:
  status: number
  body: string
run: [python3, block.py]  # command, run inside the folder. "./x.sh" is relative to the folder
timeout: 30               # seconds (default 30, max 3600)
env: [MY_API_TOKEN]       # the only environment variables the block can read
supports_dry: false       # see "Dry runs"
tests:                    # run by `sentinel block check`
  - name: refuses other schemes
    in: { url: "file:///etc/passwd" }
    error: only http      # the block must fail with a message containing this
  - name: happy path
    in: { url: "http://example.invalid" }
    out: { status: 200 }  # listed outputs must match; others are ignored
```

### The protocol

Sentinel writes one JSON object to **stdin**:

```json
{"protocol": 1, "block": "Http.Get", "workflow": "price-watch", "dry": false,
 "inputs": {"url": "https://example.com"}}
```

The block writes one JSON object to **stdout** and exits 0:

```json
{"outputs": {"status": 200, "body": "..."}, "note": "GET ... -> 200"}
```

| Reply field | Meaning |
|---|---|
| `outputs` | the declared outputs. Undeclared keys are dropped; missing ones become zero values |
| `note` | one line shown in the run log ("what happened") |
| `plan` | for dry runs: what a real run would do |
| `skipped` | `true` if the block chose not to act (a `write-external` step then frees its idempotency key so the event is retried later) |
| `error` | a failure message (same as printing to stderr and exiting non-zero) |

To fail: print the reason to **stderr** and exit non-zero. Anything else (non-JSON stdout, more
than 4 MB of output, no reply before the timeout) is a failure with a clear message.

## Effects (this is the safety model)

| `effect` | Meaning | In workflows |
|---|---|---|
| `read` | only reads | runs in dry runs too |
| `write-internal` | saves state on this machine | not executed in dry runs |
| `write-external` | changes something outside (sends, posts, pays) | needs an `idempotency_key`; listed in `write_allow` if the workflow has one; a timeout is treated as "outcome unknown" and never retried blindly |

**Dry runs.** A block that is not `read` is **not executed** in a dry run; the log says it would
run. If the block can compute a plan without acting, set `supports_dry: true`; it then receives
`"dry": true` and must only return a `plan`.

## Lifecycle commands

```bash
sentinel block new My.Block [--lang python|sh]  # scaffold a working example (pending)
sentinel block add ./path/to/My.Block           # or import a folder written elsewhere (pending)
sentinel block check My.Block                   # validate the manifest and run the tests
sentinel block show My.Block                    # print every file: review before approving
sentinel block approve My.Block                 # asks you; pins the contents
sentinel block list                             # state of every block folder
sentinel block revoke My.Block                  # stop loading it (keeps the files)
sentinel block remove My.Block                  # delete it
```

States: `pending` (proposed), `approved` (usable), `unapproved` (installed, never approved),
`modified` (changed since approval; not usable), `invalid` (manifest problem), `conflict` (name
clashes with a built-in).

Blocks live in `$SENTINEL_BLOCKS_DIR` (default `<state dir>/blocks.d`). Approvals are in
`approvals.json` next to it, or in `$SENTINEL_APPROVALS_FILE`.

## What protects you, and what does not

Protects:
- A block runs only after a person approved its exact bytes; any edit revokes that.
- `approve` refuses to run without a terminal, so an agent or script cannot approve its own block.
- The block gets a scrubbed environment: only `PATH`, `HOME`, `LANG` and the variables listed in
  `env`. A block cannot read Sentinel's webhook URL or another block's secrets.
- Timeout (process group is killed), 4 MB output cap, symlinks refused in block folders, built-in
  blocks cannot be shadowed, the declared `effect` is enforced by the validator.

Does **not** protect:
- A block is an ordinary process. It can read files and use the network as your user. Approving
  means "I read this code". Review it with `sentinel block show` first.
- An agent with write access to the blocks folder **and** to `approvals.json` could forge an
  approval. Keep the approvals file out of the agent's reach (`SENTINEL_APPROVALS_FILE`, a folder
  the agent cannot write) if the agent is not trusted.
- `check` runs `read` blocks for real (tests can hit the network). Blocks that change things are
  only tested if they support dry runs.

## Instructions for an agent writing a block

1. Check `sentinel blocks` first: use a built-in block if one fits.
2. `sentinel block new Group.Action` (or write the folder yourself and `sentinel block add` it).
3. Edit `block.yaml`: honest `effect`, every input and output typed, a clear `doc`, and tests
   that cover the happy path **and** a failure. Request only the `env` variables you need.
4. Make the program read one JSON object from stdin and write one to stdout. Keep it
   deterministic: same inputs, same outputs. No prompts, no reading the terminal.
5. `sentinel block check Group.Action` until it passes.
6. Tell the person to review (`sentinel block show Group.Action`) and approve
   (`sentinel block approve Group.Action`). Do not try to approve it yourself.
7. Only then use the block in a workflow and run `sentinel validate` and `--dry-run`.

## Compiled-in blocks (Go)

For a block that should ship inside the binary, one call is enough:

```go
engine.Register(engine.Func(engine.Spec{
    Name: "Text.Upper", Effect: engine.Read,
    Inputs:  map[string]string{"text": "string"},
    Outputs: map[string]string{"text": "string"},
    Doc:     "Upper-cases text.",
}, func(env *engine.Env, in map[string]any) (map[string]any, error) {
    return map[string]any{"text": strings.ToUpper(in["text"].(string))}, nil
}))
```

Put it in `internal/blocks` inside an `init()`. Blocks that change things must honour `env.Dry`.

## Examples

`examples/blocks/Json.Pick` turns JSON into a keyed list for `Diff`. `examples/blocks/Http.Get`
fetches a URL. `examples/plugin-demo.yaml` is a workflow using `Json.Pick`.
