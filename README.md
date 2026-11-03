# Assist

Alex's personal assistant, built on [Open Conduit](https://github.com/open-conduit/open-conduit). You give it jobs in a native Mac app; it works them in the background with Claude Code and gets back to you only when it needs you or it's done, like messaging a capable human.

- **One thread per job.** Each thread is durable Open Conduit Work: it survives restarts, waits for days without a process running, and wakes when you reply or a reminder it set comes due.
- **It bothers you only when it needs you.** The assistant chooses how loudly to say things (quiet, unread, or a notification), and asks for decisions, approvals, credentials or things only you can do as typed requests the app turns into buttons. Reply straight from the notification.
- **It asks for whatever it lacks.** New Open Conduit integrations, schedules and grants come as config change sets you approve and apply in the app. Credentials go from a secure field straight to your keychain; the assistant only sees the reference. Changes to the assistant itself are issues on this repository, worked by Open Conduit's engineering agents and merged by you.

## Layout

| Path | What |
| --- | --- |
| `extension/` | The `assist` Open Conduit extension package (Go, `protocol/sdk`): the thread route and Work kind, the agent's `assist.*` operations, your actions, the change set, and the playbook (`playbooks/assist.md`). |
| `app/` | The Mac app (SwiftUI). Talks to the daemon's local management API over its Unix socket with your operator token. |
| `config/` | The `assist` scope, the `assist-alex` scope that works this repository's issues, and the template for the assistant's standing brief. |
| `home/` | The assistant's working directory: its brief (`CLAUDE.md`), memory, thread files and your attachments. Never committed. |
| `scripts/` | `install.sh` (everything below), `build-app.sh` and `version.sh`. |

## Install

Needs Open Conduit running as a service (`open-conduit service install`), Go, and Xcode's command line tools.

```sh
make install
```

That builds and installs the extension package, restarts Open Conduit so it loads, seeds `home/`, adds the `assist` and `assist-alex` scopes (backing up the config to `config.json.before-assist`, and refusing if the file has edits the daemon hasn't applied), and installs `~/Applications/Assist.app`.

## How it fits Open Conduit

| Open Conduit | Assist |
| --- | --- |
| Work `assist.thread` | a conversation thread |
| `assist.start` / `assist.message` / `assist.answer` Events | what you say, from the app's actions |
| `assist.say`, `assist.status`, `assist.spawn` (emit) | what the assistant says, and threads it opens |
| `assist.ask` (a Wait) | a question, approval, secret or action; parks the thread until you answer or write |
| `assist.propose` / `withdraw` / `validate` / `read` | config change sets; only you apply them |
| core `wait` with a timeout | reminders and follow-ups, with no process kept alive |

## Develop

```sh
make test                      # extension: gofmt, vet, race tests (incl. a real-daemon thread test), package, lint; app: unit tests
make app                       # rebuild and reinstall Assist.app
open-conduit extension certify extension/dist/assist
```

### Versions

`VERSION` holds the release; `scripts/version.sh` stamps it into the extension package and the app at build time. A build of the commit tagged `v<VERSION>` is just the release (`0.1.0`); any other build adds the short SHA (`0.1.0+611ceeb`), and uncommitted changes add `-dirty`. The app shows the release as its version and the SHA as its build. The build fails if git can't say which commit it is. To cut a release, bump `VERSION`, commit, and tag that commit `v<VERSION>`.

## Trust

The assistant's binding runs Claude Code with a shell and your MCP connectors, as you. Its approvals and requests are how it is told to work, and the change set and secret flows are enforced by Open Conduit, but a shell running as you could reach past them. To make that boundary hard, run the binding in an [srt sandbox](https://open-conduit.github.io/open-conduit/guides/sandboxes/) with its own Claude login.
