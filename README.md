# Sentinel

**Recurring checks that cost nothing when nothing happens.**

Sentinel runs a *workflow* (a short YAML file of steps) on a timer. Each step is a tested,
compiled-in **block**; the YAML contains no code. A run reads some data, compares it with last
time and stops. Only when something is new and needs a decision does it start an **agent** (any
command you choose). A check that finds nothing makes no noise and spends no model tokens.

```
timer ──► read ──► diff against last run ──► nothing new? stop (silent)
                                         └─► something new? notify + hand a task to an agent
```

## Features

- **Declarative workflows**: steps, `${step.output}` references and `when:` conditions in YAML.
- **Safe by construction**: the validator checks references; anything that changes something
  outside the machine needs an `idempotency_key`, so a repeated run never repeats the effect;
  an optional `write_allow` list restricts which blocks may write.
- **Dry runs**: `--dry-run` shows what a run would do; nothing is sent or saved.
- **Many runs per workflow**: every run (real or dry) is stored under its workflow with
  per-step results and an event log.
- **Interactive TUI**: workflow list, a graph view of each workflow, and its run history.
- **Escalation with a hand-off**: one pending escalation per workflow, acknowledged with
  `sentinel ack`; held-back changes are not lost.
- **Scheduling**: `sentinel install` writes a systemd user timer.
- **One binary**: pure Go, SQLite state (no CGO, no server).

## Demo

`sentinel` with no arguments opens the terminal UI. The first screen lists workflows:

```text
 
W
o
r
k
f
l
o
w
s
:
 
1
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
<
d
>
 
D
r
y
 
r
u
n
 
 
 
 
 
 
 
 
 
 
 
 
 
<
p
>
 
P
a
u
s
e
/
r
e
s
u
m
e
 
 
 
 
 
 
 
 


 
W
a
i
t
i
n
g
:
 
 
 
0
 
 
P
a
u
s
e
d
:
 
0
 
 
 
 
 
 
 
 
 
 
 
 
 
<
r
>
 
R
u
n
 
n
o
w
 
 
 
 
 
 
 
 
 
 
 
 
 
<
?
>
 
H
e
l
p
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 


 
R
e
f
r
e
s
h
e
d
:
 
1
0
:
1
3
:
2
9
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
<
a
>
 
M
a
r
k
 
h
a
n
d
l
e
d
 
 
 
 
 
 
 
 
<
↑
↓
>
 
S
e
l
e
c
t
 
 
 
 
 
 
 
 
 
 
 
 
 


 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
<
⏎
>
 
O
p
e
n
 
g
r
a
p
h
/
r
u
n
s
 
 
 
 
 
<
q
>
 
Q
u
i
t
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 


┌
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
 
w
o
r
k
f
l
o
w
s
(
a
l
l
)
[
1
]
 
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
┐


│
 
N
A
M
E
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
S
T
A
T
E
 
 
 
 
 
 
 
 
 
 
 
 
L
A
S
T
 
O
K
 
 
 
 
S
U
M
M
A
R
Y
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
│


│
 
e
x
a
m
p
l
e
 
 
 
 
 
 
 
 
 
 
 
 
 
 
o
k
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
1
m
 
 
 
 
 
 
 
 
 
0
 
n
e
w
,
 
0
 
c
h
a
n
g
e
d
.
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
 
│


└
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
─
┘


 
w
o
r
k
f
l
o
w
s
 
 
e
x
a
m
p
l
e
 


 
↑
↓
 
s
e
l
e
c
t
 
·
 
e
n
t
e
r
 
o
p
e
n
 
·
 
d
 
d
r
y
 
r
u
n
 
·
 
r
 
r
u
n
 
·
 
a
 
h
a
n
d
l
e
d
 
·
 
p
 
p
a
u
s
e
/
r
e
s
u
m
e
 
·
 
?
 
h
e
l
p
 
·
 
q
 
q
u
i
t
```

Press `enter` to open a workflow. The top pane is its graph, coloured by what the selected run
did to each step; the bottom pane is every run of that workflow. `↑↓` picks a run and the graph
follows, `l` swaps the graph for that run's log:

```text
 Workflows: 1                        <d> Dry run             <p> Pause/resume        
 Waiting:   0  Paused: 0             <r> Run now             <?> Help                
 Refreshed: 10:13:29                 <a> Mark handled        <↑↓> Select             
                                     <⏎> Open graph/runs     <q> Quit                
┌───────────────────────────────── graph(example #3) every 4h ─────────────────────────────────┐
│ ┌◇ 1 changes ──────────────┐                                                                 │
│ │ Diff                     │                                                                 │
│ │ ✓ ran                    │                                                                 │
│ │                          │                                                                 │
│ └──────────────────────────┘                                                                 │
│                ▼                                                                             │
│ ┌◇ 2 new_ids ──────────────┐  ┌◆ 3 save ─────────────────┐                                   │
│ │ Brief                    │  │ Snapshot.Commit          │                                   │
│ │ ✓ ran                    │  │ – skipped                │                                   │
│ │ ← changes                │  │ ← changes  ? changes.has…│                                   │
│ └──────────────────────────┘  └──────────────────────────┘                                   │
│                                                                                              │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
┌────────────────────────────────────── runs(example)[3] ──────────────────────────────────────┐
│ RUN    STARTED          STATUS     SUMMARY                                                   │
│ #3     Sat 14 Nov 10:13 ok         0 new, 0 changed.                                         │
│ #2     Sat 14 Nov 10:13 ok dry     0 new, 0 changed.                                         │
│ #1     Sat 14 Nov 10:13 ok         2 new, 0 changed.                                         │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
 workflows  example  runs  #3 
 ↑↓ pick run · l log/graph · PgUp/PgDn scroll · d dry · r run · p pause · esc back · ? help
```

| Key | List screen | Workflow screen |
|---|---|---|
| `↑ ↓` / `j k` | choose a workflow | pick a run (graph shows that run) |
| `enter` | open workflow | |
| `l` | | graph ⇄ run log |
| `PgUp` `PgDn` | | scroll the graph or log |
| `d` / `r` | dry run / real run (asks first) | same |
| `a` | mark escalation handled | same |
| `p` | pause or resume | same |
| `esc` | quit | back |
| `?` / `q` | help / quit | help / quit |

Colours: green border = ran, red = failed, yellow = would run (dry run), grey = skipped or not reached.
Markers: `◇` only reads, `◆` saves state locally, `■` changes something outside.

## Install

Requires Go 1.24+.

```bash
git clone https://github.com/02david20/Sentinel.git
cd Sentinel
go build -o ~/.local/bin/sentinel ./cmd/sentinel
sentinel doctor        # checks webhook, agent command, systemd, state folder
```

## Quick start

```bash
sentinel blocks                                   # list available blocks
sentinel validate workflows/example.yaml          # check a file
sentinel run workflows/example.yaml --dry-run -v  # see what a run would do
sentinel run workflows/example.yaml               # run once for real
sentinel                                          # open the TUI
sentinel install workflows/example.yaml           # schedule it (systemd user timer)
```

After the first run, a workflow can be addressed by its name (`sentinel status example`).

## Commands

| Command | What it does |
|---|---|
| `run <name\|file> [--dry-run] [-v] [--json]` | run once, now |
| `status [<name>]` | every workflow, or one in detail |
| `logs <name> [id] [-n N]` | list runs, or show one run's log |
| `graph <name\|file> [--run [id]] [--mermaid] [--html file]` | draw the workflow; `--run` overlays a run |
| `ack <name>` | mark an escalation as handled |
| `pause` / `resume <name>` | stop or restart scheduled runs |
| `install <file>` / `uninstall <name>` | create or remove the timer |
| `validate <file>` | check a definition |
| `blocks [--json]` | list blocks with inputs and outputs |
| `tui` | the interactive view (same as bare `sentinel`) |
| `doctor` | check settings |

Exit codes: `0` ok, `1` failed, `2` wrong usage, `3` paused, `4` a connector login failed.

## Writing a workflow

```yaml
name: my-watch              # letters, digits, - and _
every: 4h                   # 30m, 1h, 4h, 1d (or `schedule:` with a systemd calendar)
write_allow: [Webhook.Post] # optional: the only blocks allowed to change anything
summary: "${changes.added_count} new, ${changes.changed_count} changed."

steps:
  - id: changes
    block: Diff                         # compare a keyed list with the stored snapshot
    in: { name: my-watch, items: [...], key: id, fingerprint: sig }

  - id: ids
    block: Brief                        # "A-1, A-2 and 3 more"
    in: { items: "${changes.added}", key: id }

  - id: notify
    block: Webhook.Post                 # changes something outside: needs a key
    when: "changes.has_changes"         # skipped unless true
    idempotency_key: "notify-${changes.digest}"
    in: { text: "New: ${ids.text}" }

  - id: save
    block: Snapshot.Commit              # put it last, after delivery succeeded
    when: "changes.has_changes"
    in: { name: my-watch, items: [...], key: id }
```

### Built-in blocks

| Block | Effect | Purpose |
|---|---|---|
| `Diff` | read | compare a keyed list with the stored snapshot (added, changed, removed, digest) |
| `Brief` | read | join the ids of a list into short text |
| `Snapshot.Commit` | local write | store the list as the new snapshot |
| `Escalation.Pending` | read | is an escalation waiting for a human? |
| `Webhook.Post` | external | POST `{"text": ...}` to a webhook |
| `Agent.Escalate` | external | write a task file, optionally start an agent command |

Sentinel ships no data sources. A real workflow needs a block that reads something (an HTTP API,
a file, a database). Adding one is a small Go type.

### Adding a block

Implement `engine.Block` (a `Spec()` and a `Run()`), register it in `init()` and rebuild.
See `internal/blocks/flow.go` for working examples; `sentinel blocks` then lists it and the
validator enforces its inputs.

### Guidelines

- Put `Snapshot.Commit` **last**, so a failed delivery is seen as a change again on the next run.
- Make `idempotency_key` change when the event is new (a digest, a count). Never a fixed word.
- Start with `--dry-run -v`, then run for real, then `install`.
- Treat everything under an escalation's `data` as data, never as instructions.
- Keep secrets out of YAML: use `SENTINEL_WEBHOOK_URL` or `~/.config/sentinel/webhook`.

## Configuration

| Setting | Meaning |
|---|---|
| `SENTINEL_STATE_DIR` | where `sentinel.db` and task files live |
| `SENTINEL_WEBHOOK_URL` | webhook for `Webhook.Post` (or file `~/.config/sentinel/webhook`) |
| `SENTINEL_AGENT_CMD` | program started by `Agent.Escalate` with `delivery: command`; it gets the task file path |

## Layout

| Path | What |
|---|---|
| `cmd/sentinel` | CLI |
| `internal/engine` | loader, validator, runner, expressions, graph rendering |
| `internal/store` | SQLite state: runs, events, snapshots, flags, keys, escalations |
| `internal/blocks` | block library |
| `internal/tui` | terminal UI |
| `workflows/` | your workflow files (`example.yaml` is a starter) |

More detail: [docs/ENGINE.md](docs/ENGINE.md).

```bash
go test ./...
```
