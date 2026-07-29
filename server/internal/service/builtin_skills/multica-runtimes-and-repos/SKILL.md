---
name: multica-runtimes-and-repos
description: "Use when inspecting or debugging Multica runtimes, daemon task claiming, agent not running, workdir/session reuse, or repository checkout. Covers runtime online/offline state, daemon heartbeat/claim chain, task-scoped repo checkout, project repo context, local_directory caveats, and safe diagnostic commands."
user-invocable: false
allowed-tools: Bash(multica *)
---

# Multica Runtimes and Repos

## Quick start

For "agent did not run" or "repo checkout failed", read the chain before changing anything:

```bash
multica agent get <agent-id> --output json
multica runtime list --output json
multica repo checkout <repo-url>
```

Runtime and repo commands affect active agent execution. Do not restart daemons, update runtimes, or check out arbitrary repos just to test.

## Core model

A runtime is the execution target behind an agent. A daemon owns local runtime processes and claims queued tasks from the server.

The chain is:

1. user action creates or updates an `agent_task_queue` row;
2. the task points at an agent and runtime;
3. server wakes the runtime over daemon websocket when possible;
4. daemon polls/claims the task;
5. server returns task context, repos, project resources, prior session/workdir hints, and task token;
6. daemon prepares a workdir and launches the provider CLI;
7. `multica repo checkout` talks to the local daemon, not directly to GitHub.

## CLI

```bash
multica runtime list --output json
multica runtime usage <runtime-id> --output json
multica runtime activity <runtime-id> --output json
multica task status <task-id> --output json
multica task events <task-id> --output json
multica runtime update <runtime-id> --target-version <version> --output json
multica runtime delete <runtime-id>
multica repo checkout <url>
multica repo checkout <url> --ref <branch-or-sha>
```

`task status` is the read-only reconciliation view. Read its conditions
independently: `RunActive`, `RuntimeAlive`, `ProviderAlive`, `SlotHeld`, and
`Stalled` are not interchangeable. `Unknown` means the durable evidence is
insufficient; it is not equivalent to `False`, does not prove a dead provider
or a released slot, and must not trigger an automatic rerun.

`task events` returns the append-only evidence ordered by per-task sequence.
`server.task_queue` is authoritative for queue state. Only `daemon://...`
events prove provider and slot lifecycle. `task-token://...` events are useful
for diagnostics, but the spawned agent also holds that token, so status does
not trust them as process or capacity evidence. `history_complete: false`
means the task predates ledger capture or its initial server event is missing;
do not infer the missing prefix.

Task-scoped wrappers may append low-frequency, non-secret observations with a
stable idempotency key:

```bash
multica task event add "$MULTICA_TASK_ID" \
  --id <stable-id> \
  --type journal.delivery_acked \
  --component journal \
  --time <rfc3339-time> \
  --data '{"checkpoint_id":"..."}'
```

Retry the same occurrence with the same `--id` and unchanged event fields.
Reusing an id for a different event is rejected. Do not publish heartbeats or
progress ticks into the ledger.

`runtime update` and `runtime delete` are writes. Starting a runtime update is limited to its owner or a workspace owner/admin; the original initiator may keep polling that specific in-flight request if their admin role changes. `runtime delete` removes a runtime registration; if active agents are still bound, it refuses unless the user explicitly passes `--cascade`, which archives those agents and cancels their queued/running tasks before deleting the runtime. `repo checkout` creates a dedicated branch in the task working directory. Most runtimes use a linked worktree; Linux Codex uses task-local Git metadata so its `workspace-write` sandbox can stage and commit without making the shared `.repos` cache writable.

`repo checkout` requires `MULTICA_DAEMON_PORT`; it is intended to run inside a daemon task. If absent, you are not in the normal agent checkout path. When a project `github_repo` resource has `resource_ref.ref`, `repo checkout <url>` uses that ref by default for the current task; an explicit `repo checkout <url> --ref <branch-or-sha>` overrides it.

## Debugging an agent that did not run

Check in this order:

1. Was a task supposed to be created? Inspect issue/comment/autopilot context.
2. Is the assignee an agent or squad? A squad routes to its leader.
3. Is the agent archived or bound to a runtime the actor cannot use?
4. Is the runtime online? `multica runtime list --output json`.
5. Did the daemon heartbeat recently? Runtime `last_seen_at` is the visible clue.
6. What does `multica task status <task-id> --output json` prove, and which
   conditions remain `Unknown`?
7. Does `multica task events <task-id> --output json` contain an authoritative
   provider exit or slot release, or only task-token observations?
8. Did the task get claimed or is it stuck pending/running/waiting for local directory?
9. If repo checkout failed, classify it after checking whether repo context was
   present in the task/project context.

## Repos

The runtime brief lists repos available to this task. Treat that list as the authority for agent checkout unless the user explicitly asks to bind a new project resource.

Workspace repos and project resources are not the same thing:

- workspace repo metadata can appear in workspace context;
- `github_repo` project resources are durable project context and can affect future tasks; optional `resource_ref.ref` pins the default checkout ref for tasks in that project;
- `local_directory` resources point at a path owned by a daemon and carry local-machine assumptions.

Do not add a project resource just because `repo checkout` failed. First determine whether the user asked for durable project context or just a task checkout.

More source-backed details: `references/runtimes-and-repos-source-map.md`.
