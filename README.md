# Sentinel AI Code Reviewer

An AI agent that reviews GitHub pull requests. It runs when a pull request is opened or updated, investigates the change with a set of controlled tools, and posts a review with findings backed by evidence.

It is built as an agent, not a "send the diff to an LLM" script: an orchestrator runs a bounded agent loop, and the model can act only by calling tools from a registry. Every call goes through validation and guardrails first, and anything that comes from the repository is treated as untrusted input.

> **Status:** Stage 1 of 7 (secure foundation). The agent is not implemented yet. See the [Roadmap](#roadmap).

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

## Project layout

```text
cmd/reviewer/        CLI entrypoint, run inside GitHub Actions
internal/config/     environment loading, validation, Secret type
internal/redact/     credential redaction for logs and tool output
internal/logging/    slog logger with mandatory redaction
.github/workflows/   CI: secret scan → test → lint
.githooks/           pre-commit: secret scan → fmt → vet → test
```

## Getting started

Requirements: Go 1.25+, [gitleaks](https://github.com/gitleaks/gitleaks#installing), and optionally [golangci-lint](https://golangci-lint.run/welcome/install/) v2.

```bash
make hooks              # enable the pre-commit hook (once per clone)
cp .env.example .env    # then fill in real values; .env is git-ignored
make run                # runs the reviewer with -env-file .env
make check              # secrets → vet → lint → test, same order as CI
```

`make help` lists all targets.

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
| `REPO_PATH` | no | `.` | Checked-out repository to inspect |
| `DRY_RUN` | no | `false` | Print the review instead of publishing |
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

1. **Secure foundation** *(current)*: config, redaction, logging, secret scanning, CI, pre-commit
2. **Agent core**: domain model (findings, review, validation), tool registry with schemas and risk levels, orchestrator loop with guardrails (tested against a fake LLM)
3. **Integrations**: GitHub client (PR, SHAs, files, diff), OpenAI client with tool calling and structured output, `get_diff` / `read_file` / `search_code` with path confinement
4. **MVP end-to-end**: `publish_review` (idempotent PR comment), AI review job in CI, Docker image
5. **Verification tools**: sandboxed runner (allowlisted commands, timeouts, scrubbed environment), `run_tests`, `run_linter`, `run_static_analysis` (Semgrep)
6. **Review quality**: inline comments, code-aware retrieval (changed symbols → references → implementations → tests), Tree-sitter evaluation
7. **Production readiness**: OpenTelemetry (tokens, cost, tool usage), PostgreSQL persistence, GitHub App with webhooks

## License

[MIT](LICENSE)
