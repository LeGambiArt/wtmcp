# OSIDB — Open Source Incident Database

OSIDB is a vulnerability and security flaw tracking system. It manages the lifecycle of security vulnerabilities from discovery through remediation.

## Key Concepts

**Flaw**: A security vulnerability identified by a CVE ID (e.g. CVE-2024-1234) or UUID. Flaws have:
- `impact`: CRITICAL, IMPORTANT, MODERATE, or LOW
- `workflow_state`: NEW → TRIAGE → PRE_SECONDARY_ASSESSMENT → SECONDARY_ASSESSMENT → DONE (or REJECTED)
- `source`: Where the flaw was reported from (e.g. INTERNET, CUSTOMER, UPSTREAM)
- `embargoed`: Whether the flaw is under embargo (not publicly disclosed yet)
- `owner`: Email of the person responsible for triaging/resolving the flaw
- `components`: Software components affected
- `comment_zero`: Initial description of the vulnerability

**Affect**: Records that a flaw affects a specific product/component combination:
- `ps_module`: Product stream module (e.g. rhel-9, openshift-4)
- `ps_component`: Specific component within the module
- `affectedness`: NEW, AFFECTED, or NOTAFFECTED
- `resolution`: DEFER, WONTFIX, OOSS (out of support scope), DELEGATED

**Tracker**: A bug tracking ticket (Bugzilla/Jira) filed to track the fix for an affect. Created via the "file trackers" operation from affect UUIDs.

**Label**: Workflow labels on flaws (e.g. "hybrid_bu", "core_bu") with:
- `name`: Label name
- `state`: NEW, REQ, SKIP, DONE
- `contributor`: Email of person responsible for this label's work

**CVSS Score**: Vulnerability severity score with vector string, version (V2/V3/V4), and issuer (RH/NIST/CVEORG).

## Common Query Patterns

- Find unowned critical flaws: `osidb_flaw_list` with `impact=CRITICAL`, `owner_isempty=true`
- Find flaws by CVE: `osidb_flaw_get` with the CVE ID directly
- Search flaws by text: `osidb_flaw_search` with keywords (supports "quoted phrases" and -exclusions)
- Find flaws by label: `osidb_flaw_list` with `flaw_labels=["label_name"]`
- DjangoQL queries: `osidb_flaw_list` with `query` param for complex filtering
- Check promotion readiness: `osidb_flaw_promotion_status` to see what blocks state advancement

## Important Notes

- Use `include_fields` on list queries to reduce response size — full flaw records can be very large
- Write operations require `dry_run=false` to execute (default is dry_run=true for safety)
- Updates use optimistic locking via `updated_dt` — the tool auto-fetches the current state before updating
- Embargoed flaws require special handling — most write operations inherit the flaw's embargo status
