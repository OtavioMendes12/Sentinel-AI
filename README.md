# Sentinel AI Code Reviewer

An AI agent that reviews GitHub pull requests. It runs when a pull request is opened or updated, investigates the change with a set of controlled tools, and posts a review with findings backed by evidence.

It is built as an agent, not a "send the diff to an LLM" script: an orchestrator runs a bounded agent loop, and the model can act only by calling tools from a registry. Every call goes through validation and guardrails first, and anything that comes from the repository is treated as untrusted input.

> **Status:** Stage 3 of 7 (integrations). The reviewer runs end to end against a real pull request in dry-run mode and prints its review; publishing to the pull request comes next. See the [Roadmap](#roadmap).

## Architecture (target)

```text
Pull Request (opened / synchronize / reopened)
        │
        ▼
GitHub Actions ──► Secret scan (gitleaks)  ◄── hard gate
        │
        ├──► Tests ─► Lint ─► Static analysis (Semgrep)
        ▼
Agent Orchestrator ── bounded loop (MAX_AGENT_STEPS, AGENT_TIMEOUT)
   │        ▲
   ▼        │ tool results (redacted, size-capped, marked untrusted)
  LLM ──► tool calls ──► Tool Registry ──► guardrails ──► tools
                                          get_diff · read_file · search_code
                                          run_tests · run_linter
                                          run_static_analysis · publish_review
        │
        ▼
Structured findings ─► schema validation ─► confidence filter ─► PR review
```

Prompt layering, from most to least trusted:

```text
System instructions  →  Agent policies  →  Tool definitions  →  Repository content (UNTRUSTED)
```

Repository content (code, comments, READMEs, test output) can never change instructions, grant permissions, register tools, or request secrets.

### Agent loop and guardrails

Each step is one model round trip. Every tool call passes these checks, in order, before anything runs:

1. **Exposure**: the tool exists and its risk level is allowed. The model is never offered `write` tools, and `New` refuses a policy that would allow them. Publishing is done by code, after validation.
2. **Arguments**: they are validated against the tool's schema. Unknown properties are rejected.
3. **Loop protection**: identical calls (same tool and arguments, after normalization) are refused. There is also a cap on calls per step.
4. **Execution**: the tool runs under its own timeout, and a panic is turned into an error.
5. **Output**: it is redacted, size-capped and wrapped in `<untrusted_content>` (with the delimiter escaped inside the content).

The model finishes by calling `submit_review`, which the orchestrator handles itself. The review is strictly decoded and validated. If it is invalid, the errors go back to the model so it can fix them. On the last step only `submit_review` is offered, and if the model still sends invalid findings they are dropped instead of losing the whole review. If the budget runs out, the run stops with `ErrStepLimit` and nothing is published.

### Tools

| Tool | Risk | What it does |
|---|---|---|
| `get_diff` | read | Lists the changed files, or returns one file's full diff |
| `read_file` | read | Returns numbered lines of a file (up to 400 per call), so findings cite exact lines |
| `search_code` | read | Searches the repository with ripgrep: definitions, callers, implementations, tests |

Repository access is confined:

- Files are opened through `os.Root`, so no path can resolve outside the checkout. Paths must be relative and cannot contain `..`.
- Symbolic links are refused. Otherwise a pull request could add `notes.txt -> .git/config` and read a blocked file under an innocent name.
- `.git/`, `.env*` (except `.env.example`), private keys, keystores and credential files are blocked for both reading and search.
- Commands run through the sandbox runner. Only allowlisted binaries run, resolved to absolute paths at startup. There is no shell. The child gets a minimal environment with no secrets, its output is capped, and its whole process group is killed on timeout.
- The query and path given to ripgrep are passed after `--regexp` and `--`, so they can never be read as flags. This matters because `--pre` would execute a program. ripgrep's regex engine runs in linear time, so a malicious pattern cannot hang the search.

## Project layout

```text
cmd/reviewer/        CLI entrypoint: wires GitHub, tools, agent and output
internal/agent/      orchestrator (agent loop, guardrails), prompts, submit_review
internal/tools/      tool registry, schemas, workspace confinement, get_diff/read_file/search_code
internal/domain/     Finding, Review, PullRequest: validation, confidence filter, ordering
internal/llm/        provider-agnostic model contract; openai/ adapter (Chat Completions)
internal/github/     minimal GitHub REST client (pull request, changed files, diff)
internal/sandbox/    allowlisted command runner (no shell, scrubbed env, timeouts)
internal/httpx/      bounded retries for transient HTTP failures
internal/config/     environment loading, validation, Secret type
internal/redact/     credential redaction for logs and tool output
internal/logging/    slog logger with mandatory redaction
.github/workflows/   CI: secret scan → test → lint
.githooks/           pre-commit: secret scan → fmt → vet → test
```

## Getting started

Requirements: Go 1.25+, [ripgrep](https://github.com/BurntSushi/ripgrep#installation), [gitleaks](https://github.com/gitleaks/gitleaks#installing), and optionally [golangci-lint](https://golangci-lint.run/welcome/install/) v2.

```bash
make hooks              # enable the pre-commit hook (once per clone)
cp .env.example .env    # then fill in real values; .env is git-ignored
make run                # runs the reviewer with -env-file .env
make check              # secrets → vet → lint → test, same order as CI
```

`make help` lists all targets.

### Reviewing a real pull request locally

1. Clone the repository to review and check out the pull request's **head commit**. The tools read the local checkout, so line numbers must match the diff:
   ```bash
   git fetch origin pull/<PR_NUMBER>/head && git checkout FETCH_HEAD
   ```
2. In `.env`, set `GITHUB_REPOSITORY`, `PR_NUMBER`, `REPO_PATH` (the checkout above) and `DRY_RUN=true`. A fine-grained GitHub token with read-only **Pull requests** and **Contents** permissions is enough.
3. Run `make run`. The review is printed as JSON on stdout, including the findings suppressed by `MIN_CONFIDENCE`, which helps tune the threshold. Logs go to stderr.

## Configuration

All configuration comes from environment variables. The app fails at startup when a required variable is missing, invalid, or still holds a placeholder from `.env.example`, and it reports every problem at once.

| Variable | Required | Default | Description |
|---|---|---|---|
| `OPENAI_API_KEY` | yes | — | LLM provider credential |
| `GITHUB_TOKEN` | yes | — | Token used to read the PR and publish the review |
| `GITHUB_REPOSITORY` | yes | set by Actions | `owner/name` of the repository under review |
| `PR_NUMBER` | yes | — | Pull request number |
| `OPENAI_MODEL` | no | `gpt-4.1` | Model name |
| `OPENAI_BASE_URL` | no | `https://api.openai.com/v1` | HTTPS only (HTTP allowed for localhost) |
| `OPENAI_TIMEOUT` | no | `60s` | Per-request timeout |
| `GITHUB_API_URL` | no | `https://api.github.com` | For GitHub Enterprise Server |
| `MIN_CONFIDENCE` | no | `0.75` | Findings below this are not published |
| `MAX_AGENT_STEPS` | no | `10` | Agent loop bound (hard limit 50) |
| `AGENT_TIMEOUT` | no | `5m` | Deadline for the whole review |
| `REVIEW_LANGUAGE` | no | `pt-BR` | Language of the review text (BCP 47 tag) |
| `REPO_PATH` | no | `.` | Repository checkout at the PR head commit |
| `DRY_RUN` | no | `false` | Print the review instead of publishing (required until stage 4) |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | no | `json` | `json` or `text` |

## Security model

- **Secrets come only from the environment.** Locally from `.env` (opt-in via `-env-file`), in CI from `${{ secrets.* }}`. Nothing sensitive is hard-coded.
- **Secrets cannot be printed by accident.** Credentials are held in `config.Secret`, which renders as `***` in every `fmt` verb, JSON, text encoding, and `slog`. The raw value is reachable only through an explicit `Reveal()`.
- **Logs are redacted twice.** The logger masks attributes with sensitive names (`password`, `*_token`, `authorization`, ...), then scrubs values for the configured secrets and well-known credential formats (GitHub, OpenAI, AWS, JWT, private keys, URL credentials).
- **`.env` files are refused inside GitHub Actions.** There the working directory is the pull request checkout, so a committed `.env` could otherwise redirect `OPENAI_BASE_URL` and steal the API key.
- **Endpoints must use HTTPS** and must not embed credentials.
- **CI uses least privilege.** The default token scope is `contents: read`, actions are pinned to commit SHAs, and the gitleaks binary is pinned and checksum-verified.
- **The secret scan runs first.** In CI, every later job `needs:` it. Locally, the pre-commit hook refuses to commit if gitleaks is not installed.

## Security Before Publishing

Run through this checklist before every push to a public remote:

- [ ] `.env` is not tracked: `git ls-files | grep -E '(^|/)\.env'` prints only `.env.example`
- [ ] no token, password, or key in the code: `make secrets` passes
- [ ] no personal data (names, emails, internal URLs, real IPs) in code, tests, fixtures, docs, or screenshots
- [ ] no private files (`*.pem`, `*.key`, keystores, credential JSON)
- [ ] `git diff --staged` reviewed line by line
- [ ] secret scan passed
- [ ] tests passed: `make test`

### Adding `.env` to `.gitignore` is not enough

`.gitignore` only stops **untracked** files from being added. If `.env` was committed before, it stays tracked, and **its contents stay in the Git history forever**, even after you delete the file or add it to `.gitignore`. Anyone who clones the repository can recover it with `git log -p`.

### If a secret is committed

Treat the secret as **compromised** the moment it is committed, even if it was never pushed:

1. **Revoke or rotate the credential immediately** at its provider (OpenAI dashboard, GitHub settings, cloud console). This is the only step that actually closes the exposure.
2. **Stop tracking the file**: `git rm --cached .env`, make sure it is in `.gitignore`, and commit.
3. **Clean the history if needed**: rewrite it with [`git filter-repo`](https://github.com/newren/git-filter-repo) (for example `git filter-repo --invert-paths --path .env`), then force-push and ask collaborators to re-clone. Forks and existing clones keep the old history, which is why step 1 cannot be skipped.
4. **Check whether it reached GitHub**: look at the repository's Secret Scanning alerts, the commit on github.com, Actions logs, and open PRs. Contact GitHub Support to purge cached views if needed.
5. **Generate a new credential** with the minimum scopes needed, and store it only in `.env` (locally) or GitHub Actions secrets.

## Roadmap

1. **Secure foundation** *(done)*: config, redaction, logging, secret scanning, CI, pre-commit
2. **Agent core** *(done)*: domain model (findings, review, validation), tool registry with schemas and risk levels, orchestrator loop with guardrails (tested against a fake LLM)
3. **Integrations** *(current)*: GitHub client (PR, SHAs, files, diff), OpenAI client with tool calling and structured output, `get_diff` / `read_file` / `search_code` with path confinement
4. **MVP end-to-end**: `publish_review` (idempotent PR comment), AI review job in CI, Docker image
5. **Verification tools**: sandboxed runner (allowlisted commands, timeouts, scrubbed environment), `run_tests`, `run_linter`, `run_static_analysis` (Semgrep)
6. **Review quality**: inline comments, code-aware retrieval (changed symbols → references → implementations → tests), Tree-sitter evaluation
7. **Production readiness**: OpenTelemetry (tokens, cost, tool usage), PostgreSQL persistence, GitHub App with webhooks

## License

[MIT](LICENSE)
