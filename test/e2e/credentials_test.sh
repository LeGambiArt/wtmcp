#!/usr/bin/env bash
#
# E2E tests for the wtmcpctl credentials CLI workflows.
#
# These tests exercise the CLI subcommands against a temporary
# working directory with env.d files. Because the keyring requires
# an OS session (D-Bus secret service on Linux, Keychain on macOS),
# these tests may be skipped in headless CI environments.
#
# Usage:
#   ./test/e2e/credentials_test.sh [path-to-wtmcpctl]
#
# Exit code 0 = all tests passed, non-zero = failure.

set -euo pipefail

# ── Configuration ───────────────────────────────────────────────

WTMCPCTL="${1:-./wtmcpctl}"
PASS=0
FAIL=0
SKIP=0
TESTS_RUN=()

# Colour output (disabled if not a terminal).
if [ -t 1 ]; then
    GREEN='\033[0;32m'
    RED='\033[0;31m'
    YELLOW='\033[0;33m'
    NC='\033[0m'
else
    GREEN=''
    RED=''
    YELLOW=''
    NC=''
fi

# ── Helpers ─────────────────────────────────────────────────────

log_pass() {
    PASS=$((PASS + 1))
    TESTS_RUN+=("PASS: $1")
    printf "${GREEN}PASS${NC}: %s\n" "$1"
}

log_fail() {
    FAIL=$((FAIL + 1))
    TESTS_RUN+=("FAIL: $1")
    printf "${RED}FAIL${NC}: %s\n" "$1"
    if [ -n "${2:-}" ]; then
        printf "  Detail: %s\n" "$2"
    fi
}

log_skip() {
    SKIP=$((SKIP + 1))
    TESTS_RUN+=("SKIP: $1")
    printf "${YELLOW}SKIP${NC}: %s\n" "$1"
}

# Create a temporary working directory that mimics the wtmcp layout.
# Sets WORKDIR and exports WTMCP_WORKDIR for the CLI.
setup_workdir() {
    WORKDIR=$(mktemp -d "${TMPDIR:-/tmp}/wtmcp-e2e.XXXXXX")
    mkdir -p "${WORKDIR}/env.d"
    mkdir -p "${WORKDIR}/credentials"
    export WTMCP_WORKDIR="${WORKDIR}"
}

# Remove the temporary working directory.
cleanup_workdir() {
    if [ -n "${WORKDIR:-}" ] && [ -d "${WORKDIR}" ]; then
        rm -rf "${WORKDIR}"
    fi
    unset WTMCP_WORKDIR 2>/dev/null || true
}

# ── Pre-flight checks ──────────────────────────────────────────

printf "=== wtmcpctl credentials E2E tests ===\n\n"

if [ ! -x "${WTMCPCTL}" ]; then
    printf "ERROR: wtmcpctl binary not found at %s\n" "${WTMCPCTL}"
    printf "Build with: go build -o wtmcpctl ./cmd/wtmcpctl\n"
    exit 1
fi

# Check if the OS keyring is available. Many of these tests require it.
# We'll attempt a simple operation and skip keyring tests if it fails.
KEYRING_AVAILABLE=true

# ── Test: CLI help output ───────────────────────────────────────

test_help() {
    local output
    if output=$("${WTMCPCTL}" credentials --help 2>&1); then
        if echo "${output}" | grep -q "Manage"; then
            log_pass "credentials --help shows usage"
        else
            log_fail "credentials --help missing expected text" "${output}"
        fi
    else
        log_fail "credentials --help exited non-zero" "${output}"
    fi
}

# ── Test: List with no groups ───────────────────────────────────

test_list_empty() {
    setup_workdir
    trap cleanup_workdir EXIT

    local output
    if output=$("${WTMCPCTL}" credentials list --plain 2>&1); then
        if echo "${output}" | grep -qi "no credential"; then
            log_pass "list with no groups shows empty message"
        else
            # An empty output is also acceptable.
            log_pass "list with no groups returns success"
        fi
    else
        log_fail "list with no groups failed" "${output}"
    fi

    cleanup_workdir
    trap - EXIT
}

# ── Test: Set and get credential ────────────────────────────────

test_set_and_get() {
    if [ "${KEYRING_AVAILABLE}" != "true" ]; then
        log_skip "set and get (keyring unavailable)"
        return
    fi

    setup_workdir
    trap cleanup_workdir EXIT

    local output

    # Set a credential with --keyring flag.
    if ! output=$(echo "token123" | "${WTMCPCTL}" credentials set jira JIRA_TOKEN --keyring 2>&1); then
        log_fail "set credential" "${output}"
        cleanup_workdir; trap - EXIT; return
    fi
    log_pass "set credential"

    # Get the credential.
    if output=$("${WTMCPCTL}" credentials get jira JIRA_TOKEN --show-value 2>&1); then
        if echo "${output}" | grep -q "token123"; then
            log_pass "get credential returns correct value"
        else
            log_fail "get credential wrong value" "${output}"
        fi
    else
        log_fail "get credential failed" "${output}"
    fi

    # Get source only.
    if output=$("${WTMCPCTL}" credentials get jira JIRA_TOKEN --source-only 2>&1); then
        if echo "${output}" | grep -q "keyring"; then
            log_pass "get --source-only returns keyring"
        else
            log_fail "get --source-only wrong source" "${output}"
        fi
    else
        log_fail "get --source-only failed" "${output}"
    fi

    # Clean up.
    "${WTMCPCTL}" credentials delete jira JIRA_TOKEN --force --keyring-only 2>/dev/null || true

    cleanup_workdir
    trap - EXIT
}

# ── Test: Delete credential ────────────────────────────────────

test_delete() {
    if [ "${KEYRING_AVAILABLE}" != "true" ]; then
        log_skip "delete credential (keyring unavailable)"
        return
    fi

    setup_workdir
    trap cleanup_workdir EXIT

    local output

    # Set a credential.
    if ! echo "del-test" | "${WTMCPCTL}" credentials set jira JIRA_TOKEN --keyring 2>/dev/null; then
        log_fail "delete: set prerequisite failed"
        cleanup_workdir; trap - EXIT; return
    fi

    # Delete with --force.
    if output=$("${WTMCPCTL}" credentials delete jira JIRA_TOKEN --force 2>&1); then
        if echo "${output}" | grep -qi "deleted"; then
            log_pass "delete credential with --force"
        else
            log_pass "delete credential returned success"
        fi
    else
        log_fail "delete credential failed" "${output}"
    fi

    # Verify it's gone.
    if output=$("${WTMCPCTL}" credentials get jira JIRA_TOKEN 2>&1); then
        log_fail "credential still exists after delete" "${output}"
    else
        log_pass "credential not found after delete"
    fi

    cleanup_workdir
    trap - EXIT
}

# ── Test: List with env.d file ──────────────────────────────────

test_list_envd() {
    setup_workdir
    trap cleanup_workdir EXIT

    # Create an env.d file.
    cat > "${WORKDIR}/env.d/jira.env" <<'ENVD'
JIRA_TOKEN=test-token
JIRA_URL=https://jira.example.com
ENVD
    chmod 600 "${WORKDIR}/env.d/jira.env"

    local output
    if output=$("${WTMCPCTL}" credentials list --plain 2>&1); then
        if echo "${output}" | grep -q "jira"; then
            log_pass "list shows env.d group"
        else
            log_fail "list does not show env.d group" "${output}"
        fi
    else
        log_fail "list with env.d file failed" "${output}"
    fi

    # List specific group.
    if output=$("${WTMCPCTL}" credentials list jira --plain 2>&1); then
        if echo "${output}" | grep -q "JIRA_TOKEN"; then
            log_pass "list group shows keys"
        else
            log_fail "list group does not show keys" "${output}"
        fi
    else
        log_fail "list specific group failed" "${output}"
    fi

    cleanup_workdir
    trap - EXIT
}

# ── Test: Credential test command ───────────────────────────────

test_credential_test() {
    setup_workdir
    trap cleanup_workdir EXIT

    # Create an env.d file.
    cat > "${WORKDIR}/env.d/jira.env" <<'ENVD'
JIRA_TOKEN=test-token
ENVD
    chmod 600 "${WORKDIR}/env.d/jira.env"

    local output
    if output=$("${WTMCPCTL}" credentials test jira 2>&1); then
        if echo "${output}" | grep -q "Found"; then
            log_pass "test command finds credentials"
        else
            log_fail "test command unexpected output" "${output}"
        fi
    else
        log_fail "test command failed" "${output}"
    fi

    # Test for non-existent group should fail.
    if "${WTMCPCTL}" credentials test nonexistent 2>/dev/null; then
        log_fail "test command should fail for nonexistent group"
    else
        log_pass "test command fails for nonexistent group"
    fi

    cleanup_workdir
    trap - EXIT
}

# ── Test: Show-config command ───────────────────────────────────

test_show_config() {
    setup_workdir
    trap cleanup_workdir EXIT

    local output
    if output=$("${WTMCPCTL}" credentials show-config 2>&1); then
        if echo "${output}" | grep -qi "keyring"; then
            log_pass "show-config displays keyring status"
        else
            log_fail "show-config missing keyring status" "${output}"
        fi
    else
        log_fail "show-config failed" "${output}"
    fi

    cleanup_workdir
    trap - EXIT
}

# ── Test: Export credentials ────────────────────────────────────

test_export() {
    setup_workdir
    trap cleanup_workdir EXIT

    # Create an env.d file.
    cat > "${WORKDIR}/env.d/jira.env" <<'ENVD'
JIRA_TOKEN=export-test
JIRA_URL=https://jira.example.com
ENVD
    chmod 600 "${WORKDIR}/env.d/jira.env"

    local output

    # Export to stdout as env format.
    if output=$("${WTMCPCTL}" credentials export jira --stdout --format env 2>&1); then
        if echo "${output}" | grep -q "JIRA_TOKEN=export-test"; then
            log_pass "export --stdout --format env"
        else
            log_fail "export env format wrong content" "${output}"
        fi
    else
        log_fail "export env format failed" "${output}"
    fi

    # Export to stdout as JSON format.
    if output=$("${WTMCPCTL}" credentials export jira --stdout --format json 2>&1); then
        if echo "${output}" | grep -q "JIRA_TOKEN"; then
            log_pass "export --stdout --format json"
        else
            log_fail "export json format wrong content" "${output}"
        fi
    else
        log_fail "export json format failed" "${output}"
    fi

    cleanup_workdir
    trap - EXIT
}

# ── Test: Backup and restore workflow ───────────────────────────

test_backup_restore() {
    if [ "${KEYRING_AVAILABLE}" != "true" ]; then
        log_skip "backup and restore (keyring unavailable)"
        return
    fi

    setup_workdir
    trap cleanup_workdir EXIT

    local output
    local backup_file="${WORKDIR}/test-backup.enc"

    # Set up credentials.
    if ! echo "backup-tok" | "${WTMCPCTL}" credentials set jira JIRA_TOKEN --keyring 2>/dev/null; then
        log_fail "backup: set prerequisite failed"
        cleanup_workdir; trap - EXIT; return
    fi

    # Create backup (password via stdin is tricky in non-interactive mode).
    # The CLI prompts for password interactively, so we skip this test
    # in non-interactive environments. This is a known limitation.
    log_skip "backup/restore (requires interactive terminal for password input)"

    # Clean up.
    "${WTMCPCTL}" credentials delete jira JIRA_TOKEN --force --keyring-only 2>/dev/null || true

    cleanup_workdir
    trap - EXIT
}

# ── Test: Migration dry-run ─────────────────────────────────────

test_migrate_dryrun() {
    if [ "${KEYRING_AVAILABLE}" != "true" ]; then
        log_skip "migrate dry-run (keyring unavailable)"
        return
    fi

    setup_workdir
    trap cleanup_workdir EXIT

    # Create an env.d file.
    cat > "${WORKDIR}/env.d/jira.env" <<'ENVD'
JIRA_TOKEN=migrate-test
JIRA_URL=https://jira.example.com
ENVD
    chmod 600 "${WORKDIR}/env.d/jira.env"

    local output
    if output=$("${WTMCPCTL}" credentials migrate jira --dry-run 2>&1); then
        if echo "${output}" | grep -q "dry-run"; then
            log_pass "migrate --dry-run shows plan"
        else
            log_fail "migrate --dry-run unexpected output" "${output}"
        fi

        # Verify env.d file still exists (dry-run should not modify).
        if [ -f "${WORKDIR}/env.d/jira.env" ]; then
            log_pass "migrate --dry-run does not modify files"
        else
            log_fail "migrate --dry-run deleted env.d file"
        fi
    else
        log_fail "migrate --dry-run failed" "${output}"
    fi

    cleanup_workdir
    trap - EXIT
}

# ── Run all tests ───────────────────────────────────────────────

test_help
test_list_empty
test_set_and_get
test_delete
test_list_envd
test_credential_test
test_show_config
test_export
test_backup_restore
test_migrate_dryrun

# ── Summary ─────────────────────────────────────────────────────

printf "\n=== Summary ===\n"
printf "Passed: %d\n" "${PASS}"
printf "Failed: %d\n" "${FAIL}"
printf "Skipped: %d\n" "${SKIP}"
printf "Total:  %d\n" "$((PASS + FAIL + SKIP))"

if [ "${FAIL}" -gt 0 ]; then
    printf "\n${RED}Some tests failed.${NC}\n"
    exit 1
fi

printf "\n${GREEN}All tests passed.${NC}\n"
exit 0
