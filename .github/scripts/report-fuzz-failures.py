#!/usr/bin/env python3
"""File a Linear issue for each failing target listed in LOG_DIR/failed.txt.

Fuzz output and crashing inputs may describe an exploitable bug, so the nightly
job keeps them out of public logs and this script sends them only to Linear.
The E2E and deadlock nightlies use it the same way, with a target per group.
An open issue with the same title gets a comment instead of a duplicate.

Environment: LINEAR_API_KEY, LINEAR_TEAM_KEY, LINEAR_ASSIGNEE_ID, LOG_DIR (or
FUZZ_LOG_DIR), RUN_URL, COMMIT_SHA, and optionally TITLE_PREFIX, and for
nightlies other than fuzz, KIND (what failed, e.g. "E2E testnet group") and
REPRODUCE (how to reproduce it).

LOG_DIR/failed.txt holds one "<priority> <package> <target>" line per failing
target, and LOG_DIR/<target>.log holds that target's output. A log longer than
the excerpt in the issue is also attached in full, compressed.
Adapted from celestia-app's .github/scripts/report-fuzz-failures.py.
"""
import gzip
import json
import os
import pathlib
import re
import sys
import urllib.request

API = "https://api.linear.app/graphql"
# Matrix priority (.github/workflows/fuzz-nightly.yml) to Linear priority.
PRIORITIES = {"1": 1, "2": 2, "3": 3}
MAX_LOG_CHARS = 20000
MAX_INPUT_CHARS = 8000
MAX_UPLOAD_BYTES = 50 * 1024 * 1024


def graphql(query, variables):
    request = urllib.request.Request(
        API,
        data=json.dumps({"query": query, "variables": variables}).encode(),
        headers={
            "Authorization": os.environ["LINEAR_API_KEY"],
            "Content-Type": "application/json",
        },
    )
    with urllib.request.urlopen(request, timeout=30) as response:
        body = json.load(response)
    if body.get("errors"):
        raise RuntimeError("Linear API error: " + json.dumps(body["errors"]))
    return body["data"]


def fence(text):
    return "```\n" + text.replace("```", "`​``") + "\n```"


def crashing_input(pkg, log):
    """Returns the corpus file name and full bytes of the failing input."""
    match = re.search(r"Failing input written to (\S+)", log)
    if not match:
        return None, None
    path = pathlib.Path(pkg) / match.group(1)
    try:
        return path.name, path.read_bytes()
    except OSError:
        return None, None


def upload(name, data, content_type="text/plain"):
    """Uploads data to Linear's private file storage and returns its URL.

    The issue keeps the complete corpus file, since the runner workspace is
    discarded and no public artifact may hold it.
    """
    upload_file = graphql(
        """mutation($contentType: String!, $filename: String!, $size: Int!) {
          fileUpload(contentType: $contentType, filename: $filename, size: $size) {
            uploadFile { uploadUrl assetUrl headers { key value } }
          }
        }""",
        {"contentType": content_type, "filename": name, "size": len(data)},
    )["fileUpload"]["uploadFile"]
    headers = {"Content-Type": content_type, "Cache-Control": "public, max-age=31536000"}
    headers.update({h["key"]: h["value"] for h in upload_file["headers"]})
    request = urllib.request.Request(upload_file["uploadUrl"], data=data, headers=headers, method="PUT")
    with urllib.request.urlopen(request, timeout=60):
        pass
    return upload_file["assetUrl"]


def describe(pkg, target, log):
    reproduce = os.environ.get("REPRODUCE") or (
        "Reproduce by saving the complete corpus file below as "
        f"`{pkg}/testdata/fuzz/{target}/<name>` and running "
        f"`go test -run='^{target}$' {pkg}`."
    )
    lines = [
        f"Nightly {os.environ.get('KIND', 'fuzz target')} `{target}` in `{pkg}` failed.",
        "",
        f"- Run: {os.environ['RUN_URL']}",
        f"- Commit: `{os.environ['COMMIT_SHA']}`",
        "",
        reproduce,
        "",
        "Keep failure details out of public PRs and issues; see test/fuzz/README.md.",
    ]
    name, crash = crashing_input(pkg, log)
    if crash is not None:
        lines += ["", "## Crashing input", ""]
        try:
            lines.append(f"Complete corpus file: [{name}]({upload(name, crash)})")
        except Exception as err:  # Still file the issue; say what is missing.
            lines.append(f"Uploading the corpus file failed ({err}); only the excerpt below is kept.")
        text = crash.decode(errors="replace")
        if len(text) > MAX_INPUT_CHARS:
            lines.append(f"Excerpt: first {MAX_INPUT_CHARS} of {len(text)} characters, not a valid corpus file on its own.")
            text = text[:MAX_INPUT_CHARS]
        lines += ["", fence(text)]
    lines += ["", "## Output (tail)", ""]
    if len(log) > MAX_LOG_CHARS:
        data = gzip.compress(log.encode())
        if len(data) > MAX_UPLOAD_BYTES:
            lines.append(f"The complete output is {len(data)} bytes compressed, too large to attach.")
        else:
            try:
                url = upload(f"{target}.log.gz", data, "application/gzip")
                lines.append(f"Complete output: [{target}.log.gz]({url})")
            except Exception as err:  # Still file the issue; say what is missing.
                lines.append(f"Uploading the complete output failed ({err}).")
        lines.append("")
    lines.append(fence(log[-MAX_LOG_CHARS:]))
    return "\n".join(lines)


def main():
    log_dir = pathlib.Path(os.environ.get("LOG_DIR") or os.environ["FUZZ_LOG_DIR"])
    failed_file = log_dir / "failed.txt"
    # A step other than a target failed, e.g. setup or the registry check.
    if not failed_file.exists():
        print("No failing targets recorded.")
        return
    failed = [line for line in failed_file.read_text().split("\n") if line.strip()]
    if not failed:
        print("No failing targets recorded.")
        return
    if not os.environ.get("LINEAR_API_KEY"):
        sys.exit("LINEAR_API_KEY is not set; cannot file the Linear issue.")
    team = graphql(
        "query($key: String!) { teams(filter: {key: {eq: $key}}) { nodes { id } } }",
        {"key": os.environ["LINEAR_TEAM_KEY"]},
    )["teams"]["nodes"][0]["id"]

    for line in failed:
        priority, pkg, target = line.split()
        log = (log_dir / f"{target}.log").read_text(errors="replace")
        title = f"{os.environ.get('TITLE_PREFIX', 'Nightly fuzz failure')}: {target}"
        description = describe(pkg, target, log)
        existing = graphql(
            """query($team: ID!, $title: String!) {
              issues(filter: {
                team: {id: {eq: $team}},
                title: {eq: $title},
                state: {type: {nin: ["completed", "canceled"]}}
              }) { nodes { id identifier } }
            }""",
            {"team": team, "title": title},
        )["issues"]["nodes"]
        if existing:
            graphql(
                "mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success } }",
                {"input": {"issueId": existing[0]["id"], "body": description}},
            )
            print(f"{target}: commented on {existing[0]['identifier']}")
            continue
        issue = graphql(
            """mutation($input: IssueCreateInput!) {
              issueCreate(input: $input) { issue { identifier } }
            }""",
            {"input": {
                "teamId": team,
                "title": title,
                "description": description,
                "priority": PRIORITIES.get(priority, 2),
                "assigneeId": os.environ["LINEAR_ASSIGNEE_ID"],
            }},
        )["issueCreate"]["issue"]
        print(f"{target}: created {issue['identifier']}")


if __name__ == "__main__":
    main()
