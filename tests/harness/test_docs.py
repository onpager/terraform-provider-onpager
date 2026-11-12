"""Behavior checks for generated-document drift and safe publication."""
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location(
    "onpager_docs", Path(__file__).resolve().parents[2] / "scripts/harness/docs.py"
)
docs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(docs)


class DocumentationHarnessTests(unittest.TestCase):
    def test_generator_input_uses_short_name_without_altering_schema_or_address(self):
        schema = {"provider": {"version": 0}, "resource_schemas": {"onpager_team": {"version": 0}},
                  "data_source_schemas": {"onpager_user": {"version": 0}}}
        raw = {"format_version": "1.0", "provider_schemas": {"registry.terraform.io/onpager/onpager": schema}}
        converted = docs.generator_schema(raw)
        self.assertEqual(converted, {"format_version": "1.0", "provider_schemas": {"onpager": schema}})
        self.assertEqual(set(raw["provider_schemas"]), {"registry.terraform.io/onpager/onpager"})

    def test_drift_reports_missing_extra_and_changed_pages_without_writing(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            expected, current = root / "expected", root / "current"
            for path in (expected, current):
                (path / "resources").mkdir(parents=True)
                (path / "index.md").write_text("provider\n")
            (expected / "resources/team.md").write_text("generated\n")
            (current / "resources/team.md").write_text("edited\n")
            (expected / "resources/service.md").write_text("service\n")
            (current / "resources/obsolete.md").write_text("obsolete\n")
            (current / "private-notes.md").write_text("internal notes\n")
            before = {p.relative_to(current): p.read_bytes() for p in current.rglob("*") if p.is_file()}
            self.assertEqual(docs.compare_docs(expected, current), [
                "extra: resources/obsolete.md", "missing: resources/service.md", "changed: resources/team.md"
            ])
            self.assertEqual(before, {p.relative_to(current): p.read_bytes() for p in current.rglob("*") if p.is_file()})

    def test_publication_replaces_public_pages_and_preserves_internal_notes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            expected, current = root / "expected", root / "current"
            for path in (expected, current):
                (path / "resources").mkdir(parents=True)
            (expected / "index.md").write_text("new index\n")
            (expected / "resources/team.md").write_text("new team\n")
            (current / "resources/obsolete.md").write_text("obsolete\n")
            (current / "private-notes.md").write_text("keep\n")
            (current / "internal").mkdir()
            (current / "internal/plan.md").write_text("keep plan\n")
            docs.publish_docs(expected, current)
            self.assertEqual(docs.compare_docs(expected, current), [])
            self.assertEqual((current / "private-notes.md").read_text(), "keep\n")
            self.assertEqual((current / "internal/plan.md").read_text(), "keep plan\n")

    def test_missing_nested_description_reports_the_attribute_path(self):
        provider = {
            "provider": {"block": {"description": "Provider", "attributes": {}}},
            "resource_schemas": {"onpager_example": {"block": {
                "description": "Example", "attributes": {"config": {
                    "description": "Settings", "nested_type": {"attributes": {"enabled": {"type": "bool"}}}
                }}
            }}}
        }
        with self.assertRaisesRegex(ValueError, "onpager_example.config.enabled"):
            docs.validate_descriptions(provider)

    def test_missing_data_source_description_is_rejected(self):
        provider = {
            "provider": {"block": {"description": "Provider"}},
            "data_source_schemas": {"onpager_user": {"block": {"attributes": {}}}}
        }
        with self.assertRaisesRegex(ValueError, "onpager_user"):
            docs.validate_descriptions(provider)

    def test_missing_nested_data_source_description_reports_the_attribute_path(self):
        provider = {
            "provider": {"block": {"description": "Provider"}},
            "data_source_schemas": {"onpager_user": {"block": {
                "description": "User", "attributes": {"metadata": {
                    "description": "Metadata", "nested_type": {"attributes": {"active": {"type": "bool"}}}
                }}
            }}}
        }
        with self.assertRaisesRegex(ValueError, "onpager_user.metadata.active"):
            docs.validate_descriptions(provider)

    def write_source(self, root, kind, name):
        templates = root / "templates" / kind
        templates.mkdir(parents=True, exist_ok=True)
        (templates / (name.removeprefix("onpager_") + ".md.tmpl")).write_text("Example\n")
        example = root / "examples" / kind / name
        example.mkdir(parents=True, exist_ok=True)
        files = ("resource.tf", "terraform.tf", "import.sh", "import-by-string-id.tf") if kind == "resources" else ("data-source.tf", "terraform.tf")
        for filename in files:
            (example / filename).write_text("# Example\n")

    def test_data_source_templates_must_match_the_schema(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.write_source(root, "resources", "onpager_team")
            self.write_source(root, "data-sources", "onpager_user")
            template = root / "templates/data-sources/user.md.tmpl"
            template.unlink()
            with self.assertRaisesRegex(ValueError, "Data source templates.*missing=user"):
                docs.validate_sources(root, ["onpager_team"], ["onpager_user"])
            template.write_text("User\n")
            (template.parent / "obsolete.md.tmpl").write_text("Obsolete\n")
            with self.assertRaisesRegex(ValueError, "Data source templates.*extra=obsolete"):
                docs.validate_sources(root, ["onpager_team"], ["onpager_user"])

    def test_data_source_examples_are_required_without_import_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.write_source(root, "resources", "onpager_team")
            self.write_source(root, "data-sources", "onpager_user")
            docs.validate_sources(root, ["onpager_team"], ["onpager_user"])
            for filename in ("data-source.tf", "terraform.tf"):
                example = root / "examples/data-sources/onpager_user" / filename
                example.unlink()
                with self.assertRaisesRegex(ValueError, "examples/data-sources/onpager_user/" + filename):
                    docs.validate_sources(root, ["onpager_team"], ["onpager_user"])
                example.write_text("# Example\n")

    def test_resource_import_examples_remain_required_with_data_sources(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.write_source(root, "resources", "onpager_team")
            self.write_source(root, "data-sources", "onpager_user")
            for filename in ("import.sh", "import-by-string-id.tf"):
                example = root / "examples/resources/onpager_team" / filename
                example.unlink()
                with self.assertRaisesRegex(ValueError, "examples/resources/onpager_team/" + filename):
                    docs.validate_sources(root, ["onpager_team"], ["onpager_user"])
                example.write_text("# Example\n")

    def test_schema_loading_can_bind_plugin_socket_in_a_deep_checkout(self):
        with tempfile.TemporaryDirectory() as directory:
            short_temp = Path(directory)
            root = short_temp / ("checkout-" + "x" * 90)
            (root / ".harness").mkdir(parents=True)
            (root / ".harness/terraform.tfrc").write_text("# Local override\n")
            (root / "examples/provider").mkdir(parents=True)
            (root / "examples/team-configuration").mkdir()
            (root / "templates").mkdir()
            binary = short_temp / "bin"
            binary.mkdir()
            terraform = binary / "terraform"
            # Exercise the real OS socket limit at the subprocess boundary.
            # A fake Terraform CLI keeps the harness test independent of Go builds.
            terraform.write_text("#!" + sys.executable + "\n" + '''import json
from pathlib import Path
import socket
import sys
import tempfile
with tempfile.TemporaryDirectory(prefix="plugin-") as directory:
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as listener:
        listener.bind(str(Path(directory) / "rpc"))
if sys.argv[-3:] == ["providers", "schema", "-json"]:
    print(json.dumps({"format_version": "1.0", "provider_schemas": {
        "registry.terraform.io/onpager/onpager": {
            "provider": {"block": {"description": "Provider"}}
        }
    }}))
elif sys.argv[-2:] != ["validate", "-no-color"]:
    sys.exit("Unexpected Terraform command")
''')
            terraform.chmod(0o755)
            go = binary / "go"
            go.write_text("#!" + sys.executable + "\n" + '''from pathlib import Path
import sys
if "generate" in sys.argv:
    rendered = Path(sys.argv[sys.argv.index("--rendered-website-dir") + 1])
    rendered.mkdir(parents=True)
    (rendered / "index.md").write_text("Provider\\n")
elif "validate" not in sys.argv:
    sys.exit("Unexpected Go command")
''')
            go.chmod(0o755)
            environment = {"PATH": str(binary) + os.pathsep + os.environ.get("PATH", ""),
                           "TMPDIR": str(short_temp)}
            with mock.patch.object(docs, "ROOT", root), mock.patch.object(sys, "argv", ["docs.py", "generate"]), mock.patch.dict(os.environ, environment):
                with mock.patch("sys.stdout", new_callable=io.StringIO) as output:
                    docs.main()
            self.assertIn("0 resources, 0 data sources, 2 validated examples", output.getvalue())
            self.assertEqual((root / "docs/index.md").read_text(), "Provider\n")
            self.assertEqual(list((root / ".harness").glob("docs-*")), [])

    def test_main_validates_composed_and_standalone_examples_before_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.write_source(root, "resources", "onpager_team")
            self.write_source(root, "data-sources", "onpager_user")
            (root / "examples/provider").mkdir()
            (root / "examples/provider/provider.tf").write_text("# Provider\n")
            composed = root / "examples/team-configuration"
            composed.mkdir()
            for filename in ("main.tf", "variables.tf", "outputs.tf", "terraform.tf", "providers.tf"):
                (composed / filename).write_text("# Composed configuration\n")
            (root / ".harness").mkdir()
            (root / ".harness/terraform.tfrc").write_text("# Local override\n")
            provider = {
                "provider": {"block": {"description": "Provider"}},
                "resource_schemas": {"onpager_team": {"block": {"description": "Team"}}},
                "data_source_schemas": {"onpager_user": {"block": {"description": "User"}}}
            }
            schema = root / "schema.json"
            schema.write_text(json.dumps({"format_version": "1.0", "provider_schemas": {docs.ADDRESS: provider}}))
            binary = root / "fake-bin"
            binary.mkdir()
            terraform = binary / "terraform"
            terraform.write_text("#!" + sys.executable + "\n" + '''import os
from pathlib import Path
import sys
if sys.argv[-3:] == ["providers", "schema", "-json"]:
    print(Path(os.environ["TEST_SCHEMA"]).read_text())
elif sys.argv[-2:] == ["validate", "-no-color"]:
    example = Path(sys.argv[1].removeprefix("-chdir="))
    if not example.is_dir():
        sys.exit("Example directory does not exist")
    with Path(os.environ["TEST_VALIDATIONS"]).open("a") as stream:
        stream.write(str(example) + "\\n")
    if (example / "invalid").exists():
        sys.exit("Invalid example: " + example.name)
else:
    sys.exit("Unexpected Terraform command")
''')
            terraform.chmod(0o755)
            go = binary / "go"
            go.write_text("#!" + sys.executable + "\n" + '''from pathlib import Path
import sys
if "generate" in sys.argv:
    rendered = Path(sys.argv[sys.argv.index("--rendered-website-dir") + 1])
    rendered.mkdir(parents=True)
    (rendered / "index.md").write_text("Provider\\n")
elif "validate" not in sys.argv:
    sys.exit("Unexpected Go command")
''')
            go.chmod(0o755)
            validations = root / "validations.txt"
            environment = {"PATH": str(binary) + os.pathsep + os.environ.get("PATH", ""),
                           "TEST_SCHEMA": str(schema), "TEST_VALIDATIONS": str(validations)}
            with mock.patch.object(docs, "ROOT", root), mock.patch.object(sys, "argv", ["docs.py", "generate"]), mock.patch.dict(os.environ, environment):
                for example in (composed, root / "examples/data-sources/onpager_user"):
                    invalid = example / "invalid"
                    invalid.touch()
                    with self.assertRaisesRegex(ValueError, "Invalid example: " + example.name):
                        docs.main()
                    self.assertFalse((root / "docs").exists())
                    invalid.unlink()
                validations.write_text("")
                with mock.patch("sys.stdout", new_callable=io.StringIO) as output:
                    docs.main()
                self.assertIn("1 resources, 1 data sources, 4 validated examples", output.getvalue())
            self.assertEqual(validations.read_text().splitlines(), [
                str(root / "examples/provider"), str(root / "examples/team-configuration"),
                str(root / "examples/resources/onpager_team"), str(root / "examples/data-sources/onpager_user")
            ])

    def test_failed_publication_preserves_existing_pages_when_target_is_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            expected, current = root / "expected", root / "current"
            (expected / "resources").mkdir(parents=True)
            (current / "resources/team.md").mkdir(parents=True)
            (expected / "index.md").write_text("new index\n")
            (expected / "resources/team.md").write_text("new team\n")
            (current / "index.md").write_text("old index\n")
            with self.assertRaises(OSError):
                docs.publish_docs(expected, current)
            self.assertEqual((current / "index.md").read_text(), "old index\n")
            self.assertTrue((current / "resources/team.md").is_dir())

    def test_failed_swap_restores_the_complete_previous_documentation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            expected, current = root / "expected", root / "current"
            expected.mkdir()
            current.mkdir()
            (expected / "index.md").write_text("new index\n")
            (current / "index.md").write_text("old index\n")
            (current / "private-notes.md").write_text("internal notes\n")
            replace = docs.os.replace

            def fail_new_swap(source, destination):
                if Path(source).name == "rendered" and Path(destination) == current:
                    raise OSError("injected final-swap failure")
                return replace(source, destination)

            with mock.patch.object(docs.os, "replace", side_effect=fail_new_swap):
                with self.assertRaisesRegex(OSError, "injected final-swap failure"):
                    docs.publish_docs(expected, current)
            self.assertEqual((current / "index.md").read_text(), "old index\n")
            self.assertEqual((current / "private-notes.md").read_text(), "internal notes\n")


if __name__ == "__main__":
    unittest.main()
