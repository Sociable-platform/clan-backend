# Jira Conventions

## 1. Ticket structure

| Ticket | Created by |
|---|---|
| User Story (e.g. `US-4.1 Browsing the Menu`), including its description | BA |
| Sub-tasks under the user story (FE and BE work) | FE / BE |
| Separate QA task: `[QA] US-4.1 Browsing the Menu Testing` | QA |
| Bug sub-tasks inside the QA task | QA |

BA creates and describes user stories but does not create sub-tasks. QA does not create sub-tasks inside the user story; QA creates a dedicated testing task per user story, then logs bugs as sub-tasks inside that QA task.

## 2. Titles

**FE / BE sub-tasks:** `[TEAM] Verb + what + context`
- `[FE]` frontend developer
- `[BE]` backend developer (also owns infra / AWS tasks)

**QA task:** `[QA] <User Story title> Testing`, e.g. `[QA] US-4.1 Browsing the Menu Testing`

**Bug sub-tasks:** `[TEAM] <what is broken>`. The prefix is where the bug originates (`[FE]` or `[BE]`).

| Good | Bad |
|---|---|
| `[FE] No redirect or confirmation message after successful booking` | `bug - booking page` |
| `[FE] Booked time slot still visible in available slots after booking` | `fix slot display` |
| `[BE] GET /menu returns 500 when user has no reservations` | `backend error` |

General rules:
- Sub-tasks start with an imperative verb: Add, Implement, Fix, Write, Configure.
- Bug titles describe the problem, not the fix: "No redirect after booking", not "Add redirect after booking".
- Sentence case, 80 characters or fewer, no trailing punctuation.
- One deliverable per sub-task. Split if the scope is too large.

## 3. Descriptions

### FE / BE sub-tasks

All fields are mandatory. Fill them in before moving the ticket to IN PROGRESS.

```markdown
## What to deliver
Precise deliverable in 2-3 sentences.

## Done when
- [ ] specific, testable condition
- [ ] specific, testable condition
```

Example:

```markdown
## What to deliver
Implement the clan creation endpoint with member limit validation and
the matching OpenAPI documentation.

## Done when
- [ ] POST /api/v1/clans creates a clan and returns 201
- [ ] Creating a clan beyond the member limit returns 409 clan_full
- [ ] openapi/social.yaml documents the route
- [ ] go build, go vet and go test pass in CI
```

### Bug sub-tasks (inside the QA task)

A bug report must be reproducible by anyone on the team without extra context.

```markdown
## Description
What is happening and what is the impact on the user.

## Video / Screenshot
<link or n/a>

## Preconditions
- required state before reproducing

## Steps to Reproduce
1. step
2. step
3. step

## Actual Result
- what currently happens

## Expected Result
- what should happen instead
```

## 4. Status workflow

`BLOCKED -> TO DO -> IN PROGRESS -> RESOLVED (ON REVIEW) -> QA REVIEW -> DONE`

BLOCKED can be set from any status and returns to the status it came from.

| Status | Meaning |
|---|---|
| BLOCKED | Cannot proceed. The blocker must be named in a comment |
| TO DO | Scoped, description filled in, ready to be picked up |
| IN PROGRESS | Actively being worked on by the assignee |
| RESOLVED (ON REVIEW) | MR is open and pending peer review |
| QA REVIEW | QA is verifying acceptance criteria on staging |
| DONE | All ACs met, MR merged, QA approved |

## 5. Status change comments

Add a short, factual comment every time you change a ticket's status.

**IN PROGRESS -> TO DO** (developer):

```
Moving back to To Do.
Reason: <blocked / deprioritised / scope changed>
Details: <brief explanation>
Next step: <what needs to happen before this can be picked up again>
```

**IN PROGRESS -> TO DO** (QA):

```
Moving back to To Do.
Reason: Ticket not ready - <missing AC / unclear scope / missing designs>
Needs: <what must be clarified before work begins>
```

**IN PROGRESS -> RESOLVED (ON REVIEW)** (developer):

```
MR opened.
MR: <link>
Branch: <branch name>
What changed: <one sentence summary>
```

**RESOLVED (ON REVIEW) -> QA REVIEW** (developer):

```
MR approved and merged into <target branch>.
Ready for QA.
```

**QA REVIEW -> DONE** (QA):

```
QA passed.
All ACs (Acceptance Criteria) verified.
No issues found.
```
