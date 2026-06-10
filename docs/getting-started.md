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

You need an AI client that supports MCP: **Claude Code**, **Gemini CLI**,
or **Cursor**.

Python 3.9+ is required for the Jira, Confluence, GitHub, GitLab, Jenkins,
Snyk, and Testing Farm plugins. Go plugins (Bugzilla and the Google
Workspace suite) work without Python.

**Operating system:** Linux and macOS are supported. Windows users should
use [WSL](https://learn.microsoft.com/en-us/windows/wsl/install).

## 3. Install wtmcp

### COPR (Fedora / RHEL)

```bash
sudo dnf copr enable scorreia/wtmcp
sudo dnf install wtmcp
```

### Homebrew (macOS / Linux)

```bash
brew tap legambiart/wtmcp
brew install wtmcp
```

### Build from source

See [BUILDING.md](../BUILDING.md) for instructions on building from source.

## 4. Configure Your First Plugin (Jira)

### Create the credentials directory

```bash
mkdir -p -m 700 ~/.config/wtmcp/env.d
```

The `-m 700` flag sets the correct permissions in a single step —
no separate `chmod` needed for the directory.

### Copy the example file

From the repository root:

```bash
cp env.d/jira.env.example ~/.config/wtmcp/env.d/jira.env
```

### Edit for your Jira setup

Open `~/.config/wtmcp/env.d/jira.env` and fill in the block that
matches your Jira URL. Delete or leave commented out any lines that
don't apply.

**Jira Cloud** — URL looks like `https://yourorg.atlassian.net`:

```bash
JIRA_URL=https://yourorg.atlassian.net
JIRA_AUTH_TYPE=cloud
JIRA_EMAIL=you@example.com
JIRA_TOKEN=your-api-token
```

> **Tip:** Create or copy an API token at
> <https://id.atlassian.com/manage-profile/security/api-tokens>.

**Jira Server / Data Center with personal access token** — self-hosted
instance (URL does *not* end in `.atlassian.net`):

```bash
JIRA_URL=https://jira.example.com
JIRA_AUTH_TYPE=server-token
JIRA_TOKEN=your-personal-access-token
```

**Jira Server / Data Center with Kerberos** — your organisation uses
SSO / single sign-on:

```bash
JIRA_URL=https://jira.example.com
JIRA_AUTH_TYPE=server-kerberos
```

Make sure you have an active Kerberos ticket before starting the MCP
server (`kinit` if the ticket has expired).

### Lock down the file

```bash
chmod 600 ~/.config/wtmcp/env.d/jira.env
```

> **Important:** wtmcp enforces OpenSSH-style file permissions.
> If `~/.config/wtmcp/env.d/jira.env` is readable by group or other
> users (any permission bit outside `0600`), the server refuses to load
> that credential file and disables the Jira plugin. The `env.d`
> directory itself must be mode `0700`. Both checks are enforced by
> `CheckPermissions()` in `internal/config/env.go`.

<details>
<summary>How do credentials work?</summary>

env.d files are loaded at startup and scoped by filename. The file
`jira.env` maps to the credential group named `jira` — which is exactly
what the Jira plugin declares in its `credential_group` field. Only the
Jira plugin can see the variables from `jira.env`; other plugins never
receive them.

Variables are **not** exported into the server's process environment.
They are passed only to the plugin that owns the credential group, and
only the variables listed in that plugin's `env:` manifest field are
forwarded to the handler process.

Shell-exported variables (e.g. `export JIRA_TOKEN=...` in your
`.bashrc`) have no effect on plugins. All credentials must live in
`env.d` files.

The mapping is always:

```
~/.config/wtmcp/env.d/<name>.env  →  credential group "<name>"  →  plugin(s) that declare credential_group: <name>
```

Multiple plugins can share one file — for example,
`google-calendar`, `google-drive`, and `google-gmail` all read from a
single `env.d/google.env`.

</details>
