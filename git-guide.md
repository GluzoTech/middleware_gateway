# 41. How You Should Work as Cursor Agent

You are working on a production-oriented system where architectural stability is more important than speed of implementation.

Work incrementally and maintain a clean Git history throughout development.

## 41.1 Before Making Changes

Before writing significant code:

1. Inspect the existing repository.
2. Inspect the current Git status:

```bash
git status
```

3. Inspect recent commits:

```bash
git log --oneline --decorate -10
```

4. Identify existing project conventions.
5. Read relevant existing documentation before modifying architecture.
6. Identify whether the requested change belongs to the current development phase.
7. Do not overwrite or refactor working functionality unless there is a clear reason.
8. Do not make unrelated cleanup changes during a feature implementation.
9. Do not modify architecture merely to satisfy a local implementation convenience.

If the repository already contains uncommitted user changes, **do not discard, reset, stash, or overwrite them without explicit permission**.

---

## 41.2 Work in Small, Verifiable Units

Do not implement an entire phase as one enormous change.

Break each phase into logical units.

For example, instead of:

```text
Implement authentication
```

break it into:

```text
1. Authentication configuration
2. Platform API-key validation
3. Access-token validation
4. Middleware integration
5. Authentication tests
6. Documentation
```

Each unit should leave the repository in a working or intentionally documented intermediate state.

---

## 41.3 Git Checkpoints

After completing a **coherent, tested unit of work**, create a Git commit.

Do not create commits for every individual file or trivial edit.

Good commit boundaries represent a meaningful engineering change.

Examples:

```text
chore: initialize Go service structure

feat: add application configuration

feat: add health and readiness endpoints

feat: add PostgreSQL connection layer

feat: add Redis queue abstraction

feat: add platform API key authentication

feat: add integration access token authentication

feat: add EasyEcom webhook handler

feat: add EasyEcom order DTOs

feat: add order domain model

feat: add EasyEcom order mapper

feat: add integration routing

feat: add workflow execution engine

feat: add action retry handling

feat: add workflow resume state

feat: add structured integration logging

feat: add protected log viewer

test: add webhook idempotency tests

docs: document workflow architecture
```

Prefer **Conventional Commit-style messages**:

```text
type: short description
```

Use:

```text
feat:
fix:
refactor:
test:
docs:
chore:
```

Avoid vague commits such as:

```text
update code
changes
fix stuff
final
working
new code
```

---

## 41.4 Commit Only Verified Work

Before committing, run the appropriate checks.

At minimum:

```bash
go fmt ./...
go vet ./...
go test ./...
```

If the project has integration tests:

```bash
go test -race ./...
```

when appropriate.

Also inspect:

```bash
git diff
git status
```

Make sure the commit contains only the intended changes.

Do not commit:

```text
.env
API keys
access tokens
passwords
private certificates
temporary files
IDE state
runtime logs
local workflow state
build artifacts
```

Ensure `.gitignore` covers these appropriately.

---

## 41.5 Commit After Architectural Milestones

Always create a Git checkpoint after completing a significant architectural milestone.

Recommended milestones:

```text
Phase 1
Foundation
        ↓
COMMIT

Phase 2
Authentication
        ↓
COMMIT

Phase 3
EasyEcom adapter
        ↓
COMMIT

Phase 4
Domain models
        ↓
COMMIT

Phase 5
Routing
        ↓
COMMIT

Phase 6
Workflow engine
        ↓
COMMIT

Phase 7
Queue/worker
        ↓
COMMIT

Phase 8
Dabur adapter
        ↓
COMMIT

Phase 9
Retry + resume
        ↓
COMMIT

Phase 10
Logging
        ↓
COMMIT

Phase 11
Log viewer
        ↓
COMMIT

Phase 12
Integration tests
        ↓
COMMIT
```

These commits act as **architectural recovery points**.

---

## 41.6 Never Rewrite History Without Permission

Do not perform:

```bash
git reset --hard
git clean -fd
git rebase
git commit --amend
git push --force
```

unless explicitly instructed.

Do not rewrite existing commits.

The objective is to preserve a reliable development history.

---

## 41.7 Use Branches for Experimental Architecture

If you need to experiment with a significant alternative design, create a separate branch rather than destabilizing the main development branch.

For example:

```bash
git checkout -b experiment/workflow-parallelism
```

Experiment there.

If the approach is unsuccessful, it can be abandoned without damaging the stable implementation.

For normal incremental development, avoid unnecessary branch proliferation.

---

## 41.8 Maintain a Known-Good State

At the end of every major phase, the repository should be in a known-good state.

That means:

```text
Build        ✓
Tests        ✓
Vet          ✓
Formatting   ✓
Documentation ✓
Git status   Clean
```

A phase should not be considered complete if the code only works because of undocumented local changes.

---

## 41.9 Do Not Commit Generated Runtime Data

The following should normally remain outside Git:

```text
storage/logs/
storage/workflows/
tmp/
coverage.out
*.log
.env
```

Use `.gitkeep` only if an empty directory must exist in the repository.

For example:

```text
storage/
├── logs/
│   └── .gitkeep
└── workflows/
    └── .gitkeep
```

Runtime data must never become part of the source-control history.

---

## 41.10 Protect Architectural Boundaries

Before committing a change, verify that it hasn't accidentally violated the architecture.

Examples:

### Bad

```text
Dabur integration
    ↓
imports EasyEcom DTO
```

### Good

```text
EasyEcom DTO
    ↓
EasyEcom Mapper
    ↓
Gluzo Domain
    ↓
Dabur Mapper
    ↓
Dabur DTO
```

Likewise:

* Webhook handlers must not contain Dabur business logic.
* Mappers must not make HTTP calls.
* Mappers must not access databases.
* Integration clients must not execute workflows.
* Workflow actions must not know HTTP routing details.
* Logging must not contain authentication secrets.
* Queue implementation must not leak into workflow logic.

If a new requirement appears to violate an architectural boundary, stop and explain the tradeoff before proceeding.

---

## 41.11 Before Every Commit

Perform this checklist:

```text
[ ] git status reviewed
[ ] git diff reviewed
[ ] No unrelated changes
[ ] No secrets
[ ] No credentials
[ ] No runtime logs
[ ] go fmt ./...
[ ] go vet ./...
[ ] go test ./...
[ ] Architecture boundaries preserved
[ ] Documentation updated if required
[ ] Commit message describes the actual change
```

Then create the commit.

---

## 41.12 After Every Commit

Immediately verify:

```bash
git status
git log --oneline --decorate -5
```

The working tree should normally be clean.

Then report:

```text
Commit:
<hash> <message>

Implemented:
- ...

Validation:
- go fmt ✓
- go vet ✓
- go test ./... ✓

Working tree:
Clean
```

---

## 41.13 Do Not Create Artificial Commits

Do not create commits merely because a certain number of files changed.

A commit should answer:

> "What meaningful engineering state does this commit represent?"

For example:

Good:

```text
feat: implement EasyEcom webhook authentication
```

Bad:

```text
fix: change variable name
```

unless that variable change itself represents a meaningful independent correction.

---

## 41.14 When a Change Fails

If an implementation attempt causes architectural or test failures:

1. Diagnose the problem.
2. Do not blindly continue adding patches.
3. Determine whether the design itself is wrong.
4. If necessary, revert the local changes to the last known-good state.
5. Explain the problem.
6. Propose a corrected implementation.
7. Implement the corrected approach.
8. Run the complete validation suite.
9. Commit only the corrected solution.

Do not accumulate temporary hacks simply to make tests pass.

---

## 41.15 Commit History Should Tell the Engineering Story

The Git history should eventually read approximately like:

```text
feat: add integration logging

feat: implement workflow resume state

feat: add action-level retry handling

feat: implement order synchronization workflow

feat: add Dabur order integration

feat: add EasyEcom order mapper

feat: introduce order domain model

feat: implement integration routing

feat: add EasyEcom webhook processing

feat: add integration authentication

chore: initialize Go integration gateway
```

A new engineer should be able to read the history and understand how the system evolved.

---