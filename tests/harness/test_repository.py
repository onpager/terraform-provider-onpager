"""Behavior checks for the Git publication boundary, without the real index."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/harness/repository.py"


class RepositoryHarnessTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.write(".gitignore", (ROOT / ".gitignore").read_text())
        self.write("main.go", "package main\n")
        self.write("go.mod", "module example.test/provider\n")
        self.write("README.md", "# Provider\n\n[Documentation](docs/index.md)\n")
        self.write("docs/index.md", "# Provider\n")

    def write(self, relative, content="placeholder\n"):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        return path

    def run_check(self, command="check", environment=None):
        return subprocess.run(
            [sys.executable, str(SCRIPT), command, "--root", str(self.root)],
            capture_output=True, text=True, timeout=30,
            env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1", **(environment or {})},
        )

    def assert_passes(self):
        result = self.run_check()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def assert_rejected(self, message):
        result = self.run_check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(message, result.stderr)
        return result

    def test_inventory_omits_private_artifacts_and_keeps_public_inputs(self):
        private = (
            ".harness/internal-docs/research.md", ".harness/credentials.env",
            ".local/docs/api-gaps.md", ".local/docs/superpowers/plan.md",
            ".pass", ".test_integration_credentials",
            ".codex/config.toml", ".superpowers/plan.md", "AGENTS.md",
            ".idea/workspace.xml", ".vscode/settings.json", "private.env",
            "bin/provider", "dist/provider.zip", "docs/api-gaps.md",
            "docs/releasing.md", "docs/development/setup.md", "docs/superpowers/plan.md",
            "examples/demo/.terraform/providers/cache", "examples/demo/terraform.tfstate",
            "examples/demo/terraform.tfstate.backup", "examples/demo/saved.tfplan",
            "examples/demo/saved.plan", "examples/demo/terraform.tfvars",
            "examples/demo/values.tfvars.json", "examples/demo/crash.123.log",
            "scripts/harness/__pycache__/repository.cpython-313.pyc", "coverage.out",
            "internal/client/client.test", "internal/client/cpu.prof",
        )
        public = (
            "main.go", "go.mod", "tools/go.mod", "tools/go.sum", "README.md",
            "docs/index.md", "templates/resources/team.md.tmpl",
            "examples/resources/onpager_team/import.sh",
            "examples/resources/onpager_team/import-by-string-id.tf",
            "examples/demo/terraform.tfvars.example", "examples/demo/values.tfvars.json.example",
            ".env.example", ".terraform.lock.hcl", ".github/workflows/check.yml",
            "pkg/helper/helper.go", "LICENSE",
        )
        for relative in private + public:
            if relative not in ("main.go", "go.mod", "README.md", "docs/index.md"):
                self.write(relative)
        result = self.run_check("inventory")
        self.assertEqual(result.returncode, 0, result.stderr)
        paths = set(result.stdout.splitlines())
        self.assertTrue(set(public).issubset(paths), sorted(set(public) - paths))
        self.assertFalse(set(private) & paths, sorted(set(private) & paths))
        self.assertFalse((self.root / ".git").exists())

    def test_missing_ignore_protection_fails_even_without_private_files(self):
        self.write(".gitignore", "")
        self.assert_rejected("Ignore rule missing")

    def test_tracked_ignored_file_is_rejected(self):
        subprocess.run(["git", "init", "--quiet", str(self.root)], check=True, capture_output=True)
        self.write(".harness/credentials.env", "never-print-this-secret\n")
        subprocess.run(["git", "-C", str(self.root), "add", "-f", ".harness/credentials.env"],
                       check=True, capture_output=True)
        result = self.assert_rejected("Tracked ignored file: .harness/credentials.env")
        self.assertNotIn("never-print-this-secret", result.stdout + result.stderr)

    def test_global_ignore_and_git_environment_do_not_hide_public_files(self):
        excludes = self.write(".harness/global-ignore", "main.go\nREADME.md\n")
        config = self.write(".harness/gitconfig", "[core]\n\texcludesFile = " + str(excludes) + "\n")
        result = self.run_check("inventory", {"GIT_CONFIG_GLOBAL": str(config), "GIT_DIR": "/missing/git"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("main.go", result.stdout.splitlines())
        self.assertIn("README.md", result.stdout.splitlines())

    def test_normal_source_and_automation_paths_pass(self):
        self.write("cmd/inspect/main.go", "package main\n")
        self.write("pkg/helper/helper.go", "package helper\n")
        self.write(".github/workflows/check.yml", "name: Check\n")
        self.write("LICENSE", "License text\n")
        self.write("examples/demo/terraform.tfvars.example", "team_name = \"Example\"\n")
        self.write("terraform.tfvars.example", "team_name = \"Example\"\n")
        self.write("values.tfvars.json.example", "{}\n")
        self.assert_passes()

    def test_unreviewed_top_level_directory_is_rejected(self):
        self.write("personal-notes/meeting.md", "Notes\n")
        self.assert_rejected("Unexpected publication path: personal-notes/meeting.md")

    def test_public_prose_rejects_business_narrative_terms_without_echoing_content(self):
        examples = (
            ("README.md", "A company setup.\n"),
            ("docs/index.md", "Companies configure this.\n"),
            ("templates/resources/team.md.tmpl", "Настройки компании.\n"),
            ("examples/demo/main.tf", "# Company setup.\n"),
        )
        for relative, content in examples:
            with self.subTest(relative=relative):
                previous = (self.root / relative).read_text() if (self.root / relative).exists() else None
                self.write(relative, content)
                result = self.assert_rejected("Disallowed narrative term")
                self.assertNotIn(content.strip(), result.stderr)
                if previous is None:
                    (self.root / relative).unlink()
                else:
                    self.write(relative, previous)

    def test_public_path_rejects_business_narrative_term(self):
        self.write("examples/company-bootstrap/main.tf", "# Configuration\n")
        self.assert_rejected("Disallowed narrative term in public path")

    def test_links_resolve_to_published_files_and_directories(self):
        self.write("docs/guides/setup.md", "# Setup\n")
        self.write("README.md", "[Guide](docs/guides/setup.md#setup)\n[Guides](docs/guides)\n"
                   "[Remote](https://example.test/missing)\n[Mail](mailto:ops@example.test)\n")
        self.assert_passes()

    def test_broken_inline_and_reference_markdown_links_are_rejected(self):
        for text in ("[Guide](docs/missing.md)\n", "[Guide][setup]\n\n[setup]: docs/missing.md\n"):
            with self.subTest(text=text):
                self.write("README.md", text)
                self.assert_rejected("Unpublished local link in README.md")

    def test_link_to_existing_ignored_document_is_rejected(self):
        self.write(".harness/internal-docs/notes.md", "private notes\n")
        self.write("README.md", "[Notes](.harness/internal-docs/notes.md)\n")
        self.assert_rejected("Unpublished local link in README.md")

    def test_template_links_use_the_generated_document_location(self):
        self.write("docs/guides/setup.md", "# Setup\n")
        self.write("templates/index.md.tmpl", "[Guide](guides/setup.md)\n")
        self.assert_passes()

    def test_local_filesystem_references_are_rejected(self):
        for text in ("Run `/home/developer/workspace/provider/check.sh`.\n",
                     "[Notes](file:///tmp/notes.md)\n", "Read `../onpager/docs/api.md`.\n"):
            with self.subTest(text=text):
                self.write("README.md", text)
                self.assert_rejected("Local filesystem reference")


if __name__ == "__main__":
    unittest.main()
