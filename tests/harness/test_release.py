"""Release eligibility checks against real, disposable Git histories."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[2] / "scripts/harness/release-preflight.py"


class ReleasePreflightTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.environment = {key: value for key, value in os.environ.items()
                            if not key.startswith("GIT_")}
        self.environment.update(
            GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
            GIT_AUTHOR_NAME="Release test", GIT_AUTHOR_EMAIL="release@example.test",
            GIT_COMMITTER_NAME="Release test", GIT_COMMITTER_EMAIL="release@example.test",
            PYTHONDONTWRITEBYTECODE="1",
        )
        self.git("init", "--quiet", "--initial-branch=master")
        self.first = self.commit("initial")
        self.git("update-ref", "refs/remotes/origin/master", self.first)
        self.tag = "v0.0.1"
        self.git("tag", self.tag)

    def git(self, *arguments):
        return subprocess.check_output(
            ["git", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", *arguments],
            cwd=self.root, env=self.environment, text=True, stderr=subprocess.PIPE,
            timeout=10,
        ).strip()

    def commit(self, content):
        (self.root / "source.txt").write_text(content)
        self.git("add", "source.txt")
        self.git("commit", "--quiet", "-m", content)
        return self.git("rev-parse", "HEAD")

    def check(self, **overrides):
        environment = {**self.environment, "RELEASE_TAG": self.tag,
                       "RELEASE_COMMIT": self.first, "RELEASE_BRANCH": "master",
                       **overrides}
        return subprocess.run([sys.executable, str(SCRIPT)], cwd=self.root,
                              env=environment, capture_output=True, text=True, timeout=10)

    def assert_rejected(self, message, **overrides):
        result = self.check(**overrides)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(message, result.stderr)

    def test_stable_tag_passes_without_changing_refs_or_worktree(self):
        before = (self.git("show-ref"), self.git("status", "--porcelain"))
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Release preflight passed", result.stdout)
        self.assertEqual(before, (self.git("show-ref"), self.git("status", "--porcelain")))

    def test_semver_prereleases_and_build_metadata_pass(self):
        for tag in ("v1.2.3-rc.1", "v1.2.3+build.45", "v1.2.3-beta.0+build.45"):
            with self.subTest(tag=tag):
                self.git("tag", tag)
                result = self.check(RELEASE_TAG=tag)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_malformed_versions_are_rejected(self):
        for tag in ("vlatest", "1.2.3", "v1.2", "v01.2.3", "v1.2.3-01",
                    "v1.2.3-rc.01", "v1.2.3-", "v1.2.3+", "v1.2.3-rc..1",
                    "v1.2.3\n", "v١.2.3"):
            with self.subTest(tag=tag):
                self.assert_rejected("valid v-prefixed Semantic Version", RELEASE_TAG=tag)

    def test_tagged_ancestor_of_default_branch_passes(self):
        latest = self.commit("later default branch commit")
        self.git("update-ref", "refs/remotes/origin/master", latest)
        self.git("checkout", "--quiet", "--detach", self.first)
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_default_branch_name_is_supplied_by_the_event(self):
        for branch in ("main", "release/stable"):
            with self.subTest(branch=branch):
                self.git("update-ref", "refs/remotes/origin/" + branch, self.first)
                result = self.check(RELEASE_BRANCH=branch)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_commit_outside_default_branch_is_rejected(self):
        feature = self.commit("unmerged feature")
        self.git("tag", "v0.0.2")
        self.assert_rejected("not in the default branch", RELEASE_TAG="v0.0.2",
                             RELEASE_COMMIT=feature)

    def test_missing_event_metadata_is_rejected(self):
        for name in ("RELEASE_TAG", "RELEASE_COMMIT", "RELEASE_BRANCH"):
            with self.subTest(name=name):
                self.assert_rejected("Missing release event metadata", **{name: ""})

    def test_missing_default_branch_is_rejected(self):
        self.assert_rejected("Default branch is unavailable", RELEASE_BRANCH="missing")

    def test_invalid_default_branch_name_is_rejected(self):
        self.assert_rejected("Invalid default branch", RELEASE_BRANCH="../master")

    def test_missing_tag_is_rejected(self):
        self.assert_rejected("Release tag is unavailable", RELEASE_TAG="v0.0.2")

    def test_event_sha_must_be_a_full_object_id(self):
        for commit in ("HEAD", self.first[:7], "--help", "g" * 40):
            with self.subTest(commit=commit):
                self.assert_rejected("full commit object ID", RELEASE_COMMIT=commit)

    def test_moved_tag_is_rejected(self):
        latest = self.commit("different commit")
        self.git("update-ref", "refs/remotes/origin/master", latest)
        self.git("tag", "--force", self.tag)
        self.git("checkout", "--quiet", "--detach", self.first)
        self.assert_rejected("tag does not match the event commit")

    def test_wrong_checkout_is_rejected(self):
        latest = self.commit("different checkout")
        self.git("update-ref", "refs/remotes/origin/master", latest)
        self.assert_rejected("Checked-out commit does not match")

    def test_branch_with_the_release_tag_name_is_rejected(self):
        self.git("update-ref", "refs/remotes/origin/" + self.tag, self.first)
        self.assert_rejected("branch has the same name as the release tag")

    def test_annotated_tag_resolves_to_its_commit(self):
        self.git("tag", "--annotate", "--message", "Release 0.0.2", "v0.0.2")
        result = self.check(RELEASE_TAG="v0.0.2")
        self.assertEqual(result.returncode, 0, result.stderr)
