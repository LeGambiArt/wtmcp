# Getting Started with wtmcp

```bash
# Speed Run — from zero to working in 7 commands
git clone https://github.com/LeGambiArt/wtmcp.git && cd wtmcp
make build
mkdir -p -m 700 ~/.config/wtmcp/env.d
cp env.d/jira.env.example ~/.config/wtmcp/env.d/jira.env
# Edit ~/.config/wtmcp/env.d/jira.env with your credentials
chmod 600 ~/.config/wtmcp/env.d/jira.env
./wtmcpctl agent enable claude
./wtmcpctl check
# Open Claude Code and ask: "Who am I in Jira?"
```

## 1. What Is wtmcp

wtmcp is an MCP server that connects AI assistants to the tools you use every
day — Jira, GitLab, Google Workspace, Snyk, and more. You configure it once
with your credentials; after that, your AI client can query and act on those
services without you copying and pasting context back and forth.

The tool you interact with is `wtmcpctl`. It handles setup: registering wtmcp
with your AI client, verifying that credentials are in place, and managing
OAuth flows. The server itself (`wtmcp`) is launched automatically by the AI
client — you never start it by hand.

Credentials live in `~/.config/wtmcp/env.d/`, one file per service, readable
only by you. Each file is a plain list of environment variables (`KEY=value`).
The server reads them at startup and passes only the relevant values to each
plugin — other plugins never see credentials that aren't theirs.

### Included plugins

| Plugin | What it does |
|--------|-------------|
| `jira` | Issue tracking — search, create, update, sprint tools, and export |
| `confluence` | Wiki and documentation — page search and content management |
| `gitlab` | Repositories, merge requests, pipelines, and issue tracking |
| `github` | Pull requests, issues, and task discovery across repositories |
| `google-calendar` | Calendar events, scheduling, and free/busy queries |
| `google-drive` | File metadata, search, and content export |
| `google-gmail` | Email listing, search, send, drafts, and labels |
| `google-docs` | Retrieve, summarize, and write to Google Documents |
| `snyk` | Security issues — browse vulnerabilities and manage ignores |
| `bugzilla` | Bug tracking — search, create, update, and comment on bugs |
| `testing-farm` | Test execution and system reservation |

<details>
<summary>What is MCP?</summary>

MCP (Model Context Protocol) is an open standard that lets AI assistants call
external tools. Instead of working only from text in its context window, the AI
can invoke a registered tool, get back structured data, and reason over it —
all within the same conversation.

wtmcp implements the server side of this protocol. When your AI client (Claude
Code, Cursor, etc.) needs to look something up in Jira or check a GitLab
pipeline, it calls the corresponding MCP tool. wtmcp receives the call, routes
it to the right plugin, and returns the result.

</details>

## 2. Prerequisites

The table below lists everything you need before running `make build`. Verify
each tool is available by running the command in the last column.

| Tool | Minimum version | Required for | Verify |
|------|-----------------|-------------|--------|
| [Go](https://go.dev/dl/) | 1.26+ | Building wtmcp and all Go plugins | `go version` |
| [Git](https://git-scm.com/) | any | Cloning the repository | `git --version` |
| [Python 3](https://www.python.org/downloads/) | 3.9+ | Jira, Confluence, GitHub, Jenkins, Snyk, and Testing Farm plugins | `python3 --version` |
| AI client | — | Issuing natural-language commands | — |

**Supported AI clients:** Claude Code, Gemini CLI, or Cursor.

Go plugins (Bugzilla, GitLab, and the full Google Workspace suite) do not
require Python.

**Operating system:** Linux and macOS are supported. Windows users should run
wtmcp inside [WSL](https://learn.microsoft.com/en-us/windows/wsl/install)
(Windows Subsystem for Linux).

> **Go toolchain auto-download.** If the exact Go toolchain version is not
> already installed on your system, Go will download it automatically when
> `GOTOOLCHAIN=auto` is set. Check your current setting with
> `go env GOTOOLCHAIN` and enable auto-download with
> `go env -w GOTOOLCHAIN=auto`.
