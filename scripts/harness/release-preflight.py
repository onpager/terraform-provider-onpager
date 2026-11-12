#!/usr/bin/env python3
"""Validate the release event against a full checkout, without mutations."""
import os
import re
import subprocess
import sys


NUMBER = r"(?:0|[1-9][0-9]*)"
PRERELEASE = r"(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
SEMVER_TAG = re.compile(
    rf"v{NUMBER}\.{NUMBER}\.{NUMBER}"
    rf"(?:-{PRERELEASE}(?:\.{PRERELEASE})*)?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?"
)


def git(*arguments):
    return subprocess.run(["git", *arguments], capture_output=True, text=True, timeout=30)


def resolve(reference, error):
    result = git("rev-parse", "--verify", reference + "^{commit}")
    if result.returncode:
        raise ValueError(error)
    return result.stdout.strip()


def main():
    tag = os.environ.get("RELEASE_TAG", "")
    commit = os.environ.get("RELEASE_COMMIT", "")
    branch = os.environ.get("RELEASE_BRANCH", "")
    if not all((tag, commit, branch)):
        raise ValueError("Missing release event metadata: RELEASE_TAG, RELEASE_COMMIT or RELEASE_BRANCH.")
    if not SEMVER_TAG.fullmatch(tag):
        raise ValueError("Release tag must be a valid v-prefixed Semantic Version.")
    if not re.fullmatch(r"(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})", commit):
        raise ValueError("Release event must specify a full commit object ID.")
    if git("check-ref-format", "refs/heads/" + branch).returncode:
        raise ValueError("Invalid default branch name in release event.")

    commit = commit.lower()
    tagged = resolve("refs/tags/" + tag, "Release tag is unavailable; use a full checkout.")
    if tagged != commit:
        raise ValueError("Release tag does not match the event commit.")
    if resolve("HEAD", "Checked-out commit is unavailable.") != commit:
        raise ValueError("Checked-out commit does not match the event commit.")

    default = "refs/remotes/origin/" + branch
    resolve(default, "Default branch is unavailable; fetch the complete origin history.")
    collision = git("show-ref", "--verify", "--quiet", "refs/remotes/origin/" + tag)
    if collision.returncode == 0:
        raise ValueError("A branch has the same name as the release tag.")
    if collision.returncode != 1:
        raise ValueError("Unable to check for a branch matching the release tag.")
    if git("merge-base", "--is-ancestor", commit, default).returncode:
        raise ValueError("Release commit is not in the default branch.")
    print("Release preflight passed: valid version and default-branch commit.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.TimeoutExpired) as error:
        print("Release preflight failed: " + str(error), file=sys.stderr)
        sys.exit(1)
