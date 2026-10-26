#!/usr/bin/env python3
"""Re-create unsigned commits on Mergify backport PRs as verified commits.

Mergify cherry-picks backports without signing them, so branch protection that
requires signed commits blocks the PRs. For each open Mergify backport PR with
an unverified commit, this replays the PR's commits through GitHub's
createCommitOnBranch mutation, which GitHub signs, and then points the PR branch
at the replayed commits. The replay must reproduce every original tree exactly,
otherwise the PR is left untouched.

Usage: sign-mergify-backports.py [PR_NUMBER]
Requires GH_TOKEN (a token whose pushes trigger CI) and GITHUB_REPOSITORY, and
must run inside a clone of the repository.
"""
import base64
import json
import os
import subprocess
import sys

REPO = os.environ["GITHUB_REPOSITORY"]
MERGIFY = "mergify[bot]"
BACKPORT_PREFIX = "mergify/bp/"


def run(*args, data=None):
    return subprocess.check_output(args, input=data).decode()


def api(path, *args):
    return json.loads(run("gh", "api", path, *args))


def paginate(path):
    # --slurp wraps each page in an outer list.
    pages = json.loads(run("gh", "api", "--paginate", "--slurp", path))
    return [item for page in pages for item in page]


def push(sha, ref, expected):
    auth = base64.b64encode(
        ("x-access-token:" + os.environ["GH_TOKEN"]).encode()).decode()
    # Pass the credential through the environment rather than argv.
    env = dict(os.environ, GIT_CONFIG_COUNT="1",
               GIT_CONFIG_KEY_0="http.https://github.com/.extraheader",
               GIT_CONFIG_VALUE_0="AUTHORIZATION: basic " + auth)
    subprocess.check_call(
        ["git", "push", "--quiet",
         "--force-with-lease=refs/heads/{}:{}".format(ref, expected),
         "https://github.com/{}.git".format(REPO),
         "{}:refs/heads/{}".format(sha, ref)], env=env)


def graphql(payload):
    result = json.loads(run("gh", "api", "graphql", "--input", "-",
                            data=json.dumps(payload).encode()))
    if result.get("errors"):
        raise RuntimeError(json.dumps(result["errors"]))
    return result["data"]


def git(*args):
    return subprocess.check_output(["git", *args])


def file_changes(parent, commit):
    entries = git("diff", "--name-status", "--no-renames", "-z",
                  parent, commit).split(b"\0")
    additions, deletions = [], []
    for index in range(0, len(entries) - 1, 2):
        status, path = entries[index].decode(), entries[index + 1].decode()
        if status == "D":
            deletions.append({"path": path})
        elif status in ("A", "M"):
            additions.append({"path": path, "contents": base64.b64encode(
                git("show", commit + ":" + path)).decode()})
        else:
            raise RuntimeError("unsupported change " + status + " " + path)
    return {"additions": additions, "deletions": deletions}


def message(commit):
    # The signed commit is authored by the token owner, so credit the original
    # author with a trailer.
    text = commit["commit"]["message"].rstrip()
    author = commit["commit"]["author"]
    trailer = "Co-authored-by: {} <{}>".format(author["name"], author["email"])
    if trailer not in text:
        text += ("\n" if "\nCo-authored-by: " in text else "\n\n") + trailer
    headline, _, body = text.partition("\n")
    return {"headline": headline, "body": body.strip("\n")}


def create_commit(branch, head, commit, parent):
    data = graphql({
        "query": """mutation($input: CreateCommitOnBranchInput!) {
          createCommitOnBranch(input: $input) {
            commit { oid tree { oid } signature { isValid } }
          }
        }""",
        "variables": {"input": {
            "branch": {"repositoryNameWithOwner": REPO, "branchName": branch},
            "expectedHeadOid": head,
            "message": message(commit),
            "fileChanges": file_changes(parent, commit["sha"]),
        }},
    })
    created = data["createCommitOnBranch"]["commit"]
    if created["tree"]["oid"] != commit["commit"]["tree"]["sha"]:
        raise RuntimeError("replayed tree differs from " + commit["sha"])
    if not (created.get("signature") or {}).get("isValid"):
        raise RuntimeError("GitHub did not sign the replay of " + commit["sha"])
    return created["oid"]


def sign(pr):
    number, ref, head = pr["number"], pr["head"]["ref"], pr["head"]["sha"]
    commits = paginate("repos/{}/pulls/{}/commits?per_page=100".format(REPO, number))
    if all(c["commit"]["verification"]["verified"] for c in commits):
        return
    if any(len(c["parents"]) != 1 for c in commits):
        raise RuntimeError("has merge commits; sign it by hand")
    if commits[-1]["sha"] != head:
        raise RuntimeError("head moved while listing commits")

    # The replay diffs each commit against its parent, so fetch the parent too.
    git("fetch", "--quiet", "--no-tags", "--depth", str(len(commits) + 1),
        "origin", head)
    parent = commits[0]["parents"][0]["sha"]
    temp = "mergify-sign/pr-{}".format(number)
    # Clear a temp branch left behind by an interrupted run.
    subprocess.run(["gh", "api", "-X", "DELETE", "repos/{}/git/refs/heads/{}".format(
        REPO, temp)], capture_output=True)
    api("repos/{}/git/refs".format(REPO), "-f", "ref=refs/heads/" + temp,
        "-f", "sha=" + parent)
    try:
        new_head = parent
        for commit in commits:
            new_head = create_commit(temp, new_head, commit, parent)
            parent = commit["sha"]
        git("fetch", "--quiet", "--no-tags", "origin", new_head)
        # The lease rejects the update if anything was pushed to the PR since
        # we read its head, instead of overwriting it.
        push(new_head, ref, head)
        print("#{}: replaced {} with verified {}".format(number, head, new_head))
    finally:
        run("gh", "api", "-X", "DELETE",
            "repos/{}/git/refs/heads/{}".format(REPO, temp))


def main():
    if len(sys.argv) > 1:
        prs = [api("repos/{}/pulls/{}".format(REPO, sys.argv[1]))]
    else:
        prs = paginate("repos/{}/pulls?state=open&per_page=100".format(REPO))
    failed = False
    for pr in prs:
        if (pr["state"] != "open" or pr["user"]["login"] != MERGIFY
                or pr["head"]["repo"] is None
                or pr["head"]["repo"]["full_name"] != REPO
                or not pr["head"]["ref"].startswith(BACKPORT_PREFIX)):
            continue
        try:
            sign(pr)
        except (RuntimeError, subprocess.CalledProcessError) as err:
            print("::error::#{}: {}".format(pr["number"], err))
            failed = True
    sys.exit(1 if failed else 0)


main()
