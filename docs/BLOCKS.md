# Writing blocks

Sentinel's steps are **blocks**. Built-in blocks are Go code compiled into the binary. You can
also add blocks **without recompiling**: a block is a folder with a manifest and an executable
(any language). Sentinel runs it as a child process, one JSON request in, one JSON reply out.

An agent can write such a block when no built-in one fits. Nothing it writes runs in a scheduled
workflow until a **person approves the exact contents**.

```
agent writes block ──► sentinel block add/new ──► _pending/   (cannot run in workflows)
                              │
                 sentinel block check           (manifest + tests; runs the code, without secrets)
                 sentinel block show            (a human reads the code)
                 sentinel block approve         (a human, at a terminal: summary, diff, confirmation,
                              │                  THEN tests, then the exact hash is pinned)
                              ▼
                       approved (hash pinned) ──► loaded; hashed again before every execution
                              │
        any later edit to the folder ──► "modified": stops loading until approved again
```

## The folder

```
Http.Get/
  block.yaml     manifest (below)
  block.py       the program: python3, sh, a compiled binary, anything executable
```

The folder name must equal the block name. Names are capitalised words joined by dots, like
`Http.Get` or `Report.Weekly.Build`; at least one dot, no underscores.

Everything the block runs must be inside its folder: `run` is either a bare command found on
`PATH` (an interpreter such as `python3` or `sh`) or a path inside the folder, and no argument
may be absolute or contain `..`. Every file in the folder, including hidden ones and
`__pycache__`, is covered by the approval hash.

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
tests:                    # run by `sentinel block check` and again by `approve`
  - name: refuses other schemes
    in: { url: "file:///etc/passwd" }
    error: only http      # the block must fail with a message containing this
  - name: happy path
    in: { url: "http://example.invalid" }
    out: { status: 200 }  # listed outputs must match; others are ignored
  # A block that changes things can only be tested in a dry run:
  # - { name: plans only, dry: true, in: { ... }, out: { ... } }
```

Output values must match their declared types (`count: number` cannot come back as `"7"`).
`block check` never passes the `env:` variables to the block: unreviewed code does not see secrets.

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
sentinel block new My.Block [--lang python|sh] [--effect read|write-internal|write-external]
                                                # scaffold a working example with tests (pending)
sentinel block add ./path/to/My.Block           # or import a folder written elsewhere (pending)
sentinel block check My.Block                   # validate the manifest and run the tests (runs its code)
sentinel block show My.Block                    # print every file: review before approving
sentinel block approve My.Block                 # summary, diff, confirmation, then tests, then pin
sentinel block list                             # state of every block folder
sentinel block revoke My.Block                  # stop loading it (keeps the files)
sentinel block remove My.Block                  # delete it
```

States: `pending` (proposed), `approved` (usable), `unapproved` (installed, never approved),
`modified` (changed since approval; not usable), `invalid` (manifest problem), `conflict` (name
clashes with a built-in).

`sentinel validate` and `run` tell you when a workflow uses a block that is not approved, with the
exact commands to fix it, and `validate` exits with code **5** in that case.

Blocks live in `$SENTINEL_BLOCKS_DIR` (default `<state dir>/blocks.d`). Approvals are in
`approvals.json` next to it, or in `$SENTINEL_APPROVALS_FILE`.

## The approval step

`sentinel block approve My.Block` needs a person at a terminal and shows, **before running any
of the block's code**:

- the effect, the command it runs, every file with its line count and a short hash;
- the environment variables it may read (real runs only), and whether it takes part in dry runs;
- text-search hints about what the code seems to do (network, starting programs, credential-like
  paths, writing or deleting files). These are hints, not an analysis;
- on a **re-approval**, a diff against the version you approved before ("Changed since you last
  approved it"), or "Identical" if nothing changed.

You confirm with `y`. For a block that is `write-external` or reads environment variables you must
**type the block's name**. Only then are the block's tests run; if they fail nothing is approved.
The exact hash you were shown is what gets pinned: if the folder changed in the meantime the
approval is refused. A copy of what you approved is kept so the next change can be shown as a diff.

## What protects you, and what does not

Protects:
- A block runs only after a person approved its exact contents. The hash covers every file (names,
  executable bit, bytes), nothing is skipped, and symbolic links, pipes and devices are refused.
- The hash is checked again at load time **and right before every execution**.
- The command is confined to the block's folder (see "The folder").
- `approve` shows what it is approving before any of the block's code runs, pins what it showed,
  and `check` runs without the block's secrets.
- The block runs in its own session (no controlling terminal) with a scrubbed environment: `PATH`,
  `HOME`, `LANG`, `SENTINEL_BLOCK`, Python settings that avoid bytecode and user packages, plus the
  variables its manifest lists (never `SENTINEL_*`). It cannot read Sentinel's webhook URL.
- A timeout kills the whole process group, and so does every normal exit, so a block cannot leave
  background processes behind. Output is capped (4 MB stdout, 4 KB stderr), and terminal escape
  sequences in a block's notes and errors are stripped.
- Approvals are written atomically under a lock, through a private temporary file, and a symlinked
  approvals file is refused. Built-in blocks cannot be shadowed.
- A write-external block that is cancelled or times out is treated as "outcome unknown": its
  idempotency key stays claimed and it is not retried blindly.

Does **not** protect:
- A block is an ordinary process. It can read files and use the network as your user. The `effect`
  it declares is its own claim and is not enforced; only dry-run handling is. Approving means "I
  read this code". Use `sentinel block show`.
- The "needs a terminal" rule stops a script from approving by accident. It is **not** a security
  boundary: anything with a pseudo-terminal can type `y`. An agent that can write to the
  blocks folder **and** to the approvals file could forge an approval. If the agent is not
  trusted, keep the approvals file (and its `_snapshots` folder) where the agent cannot write,
  with `SENTINEL_APPROVALS_FILE`, and approve from a separate login.
- `check` runs the block for real (a `read` block can use the network during its tests) and
  blocks that change things only run in tests if they support dry runs.
- A block that keeps state inside its own folder will show as `modified`. Keep state elsewhere.
- Scheduled runs (systemd) do not inherit your shell: variables a block lists in `env:` must be set
  for the timer. `sentinel install` carries `SENTINEL_STATE_DIR`, `SENTINEL_BLOCKS_DIR` and
  `SENTINEL_APPROVALS_FILE` into the unit and lists the blocks that need extra variables.

## Instructions for an agent writing a block

1. Check `sentinel blocks` first: use a built-in block if one fits.
2. `sentinel block new Group.Action` (or write the folder yourself and `sentinel block add` it).
3. Edit `block.yaml`: honest `effect`, every input and output typed, a clear `doc`, and tests
   that cover the happy path **and** a failure (the scaffold has both). Request only the `env`
   variables you need. If the effect is not `read`, handle `"dry": true` by returning a `plan`
   only, set `supports_dry: true`, and mark the tests `dry: true`
   (`sentinel block new X.Y --effect write-external` scaffolds this).
4. Make the program read one JSON object from stdin and write one to stdout. Keep it
   deterministic: same inputs, same outputs. No prompts, no reading the terminal.
5. `sentinel block check Group.Action` until it passes.
6. Tell the person to review (`sentinel block show Group.Action`) and approve
   (`sentinel block approve Group.Action`). Do not try to approve it yourself or touch the
   approvals file. Do not edit the block after it is approved: that revokes the approval.
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
