# Git Conventions & Practices

## 1. Branching

Format: `<category>/<jira-ticket-id>-<short-description>`

- Category is lowercase.
- Jira ticket ID is uppercase, as it appears in Jira.
- Description is lowercase, words separated by hyphens, 2 to 4 words.
- Exception: `release` and `chore` branches do not require a Jira ticket ID.

```
feat/EPMTPERS-5435-user-registration
fix/EPMTPERS-5467-login-redirect
test/EPMTPERS-5435-auth-unit-tests
docs/EPMTPERS-5440-api-spec-update
ci/EPMTPERS-5441-frontend-lint-job
refactor/EPMTPERS-5450-auth-hook-split
chore/claude-rules
release/1.0.0
```

| Category | When to use |
|---|---|
| `feat` | New feature or functionality |
| `fix` | Bug fix |
| `hotfix` | Critical fix branched from `main`, merged back to both `main` and `develop`. Urgent production issues only |
| `refactor` | Code restructuring, no feature or behavior change |
| `test` | Adding or updating tests |
| `docs` | Documentation changes only |
| `ci` | CI/CD pipeline configuration |
| `chore` | Dependency updates, tooling, project configuration |
| `release` | Sprint release candidate, created at the end of each sprint |

Long-lived branches:

| Branch | Purpose | Who can push directly |
|---|---|---|
| `main` | Production-ready code. Receives merges from release branches only | Maintainers only |
| `develop` | Integration branch. All feature and fix branches merge here first | Maintainers only |

## 2. Commit messages

Conventional Commits: `<type>(<scope>): <short description>`

- Type is lowercase.
- Scope is optional but recommended: the feature or module name (`platform`, `social`, `httpx`, `jwt`, `compose`).
- Description is imperative, present tense ("add", not "added").
- No period at the end.
- First line is 72 characters maximum.

| Type | When to use |
|---|---|
| `feat` | New feature or functionality |
| `fix` | Bug fix |
| `docs` | Documentation changes only |
| `style` | Formatting only, no logic change |
| `refactor` | Code restructuring, no feature or bug change |
| `test` | Adding or updating tests |
| `ci` | CI pipeline configuration |
| `chore` | Build process, dependency updates, tooling |
| `perf` | Performance improvements |

```
feat(social): add clan creation endpoint
fix(platform): correct api key expiry check
docs(readme): update project setup instructions
ci: add lint job to pipeline
test(httpx): add unit tests for cors middleware
refactor(social): extract member limit check into service
```

## 3. Merge Request (MR)

**Title** follows the commit format: `<type>(<scope>): <short description>`.

**Description template:**

```markdown
## Summary
Brief description of what this MR does and why.

## Changes
- Change 1
- Change 2

## Type
- [ ] Feature
- [ ] Bug fix
- [ ] Refactor
- [ ] Documentation
- [ ] CI / Configuration

## Notes (optional)
Anything the reviewer should know: edge cases, dependencies, follow-up tasks.
```

**Size:**
- Keep MRs small and focused: one feature or one fix per MR. Split large features.
- Use a draft MR to show work in progress without requesting a review.
- Mark ready only when all tasks are complete and the MR is self-reviewed.

**Merge strategy:**
- Squash on merge (checked by default). Keep it checked unless there is a specific reason not to.
- The squash commit message follows Conventional Commits and matches the MR title.

## 4. Code review

Flow before merging into `develop`:

1. Developer opens the MR and shares the link in the group chat, mentioning the reviewer(s).
2. Teammate(s) review within working hours: thumbs up if good, thumbs down with comments if changes are needed.
3. Developer addresses all comments and resolves discussions.
4. Team Lead does the final review and gives the official Approve. The developer then merges their own MR into `develop`.

Only the Team Lead gives a formal GitLab approval. Teammates signal with thumbs up/down only.

**Giving feedback:**
- Be specific: reference the exact line or function.
- Distinguish blocking issues from suggestions with prefixes: `blocking:` (must fix), `suggestion:` (optional).
- Keep the tone professional and constructive.

## 5. General practices

- Never push directly to `main` or `develop`. Use a branch and open an MR.
- Pull the latest `develop` before creating a new branch.
- One feature or fix per branch. Keep branches short-lived.
- Delete branches after they are merged.
- Resolve merge conflicts locally before pushing.
- Do not commit sensitive data: API keys, passwords, tokens, `.env`, `*.pem`.
- Keep history clean: do not commit broken or unfinished code (`go build`, `go vet`, `go test` must pass).
- Discuss in MR comments; do not resolve discussions over chat.
