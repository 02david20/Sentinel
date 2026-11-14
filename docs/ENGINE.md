# Engine guide

## Workflow file

```yaml
name: my-watch                 # letters, digits, - and _ only
every: 4h                      # 30m, 1h, 4h, 1d (or `schedule:` with a systemd OnCalendar expression)
write_allow: [Webhook.Post]      # optional: the only blocks allowed to change anything
summary: "${found.count} items."

steps:
  - id: found
    block: SomeBlock
    in: { ... }

  - id: say
    block: Webhook.Post
    when: 'found.count > 0'    # optional expr; false = skip the step
    idempotency_key: "say-${found.count}"
    in: { text: "${found.count} items" }
```

## Rules the validator enforces

- `${step.output}` may only refer to a step above it. `sentinel blocks` lists each block's outputs.
- A block whose effect is "changes something outside" needs an `idempotency_key` that changes
  when the event is new (a digest, a count), never a fixed word.
- `when` must evaluate to true or false.
- If `write_allow` is present, every writing block must be listed.

## Block effects

`Read` (no side effects), `WriteInternal` (state on this machine; skipped in dry runs),
`WriteExternal` (needs an idempotency key; dry runs only print the plan).

## Escalation

`Agent.Escalate` writes a task file and optionally starts the command in `$SENTINEL_AGENT_CMD` with the task file as its argument (`delivery: command`). Only one escalation
per workflow is pending at a time; clear it with `sentinel ack <name>`. `Escalation.Pending` lets a
workflow check for one.

## Auth failures

A block may return `engine.AuthError` to pause the workflow. The run exits with code 4 and the
workflow stays paused until `sentinel resume <name>`.

## State

SQLite file `sentinel.db` in `SENTINEL_STATE_DIR` (default next to `store.DefaultPath()`). `sentinel logs <name> [id]`
reads run events; `sentinel graph <name> [--run] [--mermaid]` draws the workflow.

## Runs and the TUI

Every execution of a workflow is a run stored under that workflow (id, start, status, per-step
results, event log; dry runs are marked). `sentinel logs <name>` lists them, `sentinel logs <name> <id>` shows one.
In `sentinel tui`, `enter` opens a workflow: the top pane draws its steps as a layered graph
(boxes per step, arrows between layers) coloured by what the selected run did; the bottom pane
lists the runs. `↑↓` picks a run, `l` swaps the graph for that run's log, `PgUp/PgDn` scrolls, `esc` goes back.
