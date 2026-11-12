#!/usr/bin/env python3
"""Generate or check Registry pages using the real local provider schema."""
from pathlib import Path
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from urllib.parse import unquote, urlsplit


ROOT = Path(__file__).resolve().parents[2]
ADDRESS = "registry.terraform.io/onpager/onpager"
PUBLIC_DIRS = ("resources", "guides", "data-sources", "functions", "actions",
               "ephemeral-resources", "list-resources", "state-stores")


def generator_schema(document):
    # tfplugindocs 0.25.0 accepts short-name/hashicorp keys for supplied JSON.
    # Use its supported short-name input; preserve the actual exported schema.
    return {"format_version": document["format_version"],
            "provider_schemas": {"onpager": document["provider_schemas"][ADDRESS]}}


def public_files(directory):
    files = {}
    index = directory / "index.md"
    if index.is_file():
        files[Path("index.md")] = index.read_bytes()
    for name in PUBLIC_DIRS:
        folder = directory / name
        if folder.exists():
            for path in folder.rglob("*"):
                if path.is_file():
                    files[path.relative_to(directory)] = path.read_bytes()
    return files


def compare_docs(expected, current):
    wanted, actual = public_files(expected), public_files(current)
    differences = []
    for path in sorted(set(wanted) | set(actual)):
        if path not in wanted:
            differences.append("extra: " + path.as_posix())
        elif path not in actual:
            differences.append("missing: " + path.as_posix())
        elif wanted[path] != actual[path]:
            differences.append("changed: " + path.as_posix())
    return differences


def publish_docs(expected, current):
    """Prepare a complete tree and restore the previous one if swapping fails."""
    cache = current.parent / ".harness"
    cache.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="docs-publish-", dir=cache))
    rendered, previous = work / "rendered", work / "previous"
    keep_backup = False
    try:
        if current.exists():
            shutil.copytree(current, rendered)
        else:
            rendered.mkdir()
        write_public_pages(expected, rendered)
        try:
            if current.exists():
                os.replace(current, previous)
            os.replace(rendered, current)
        except BaseException:
            if previous.exists():
                try:
                    os.replace(previous, current)
                except OSError as error:
                    keep_backup = True
                    raise ValueError("Could not restore documentation; previous files retained at " + str(previous)) from error
            raise
    finally:
        if not keep_backup:
            shutil.rmtree(work, ignore_errors=True)


def write_public_pages(expected, current):
    """Update only Registry pages in a disposable copy of the documentation."""
    wanted, actual = public_files(expected), public_files(current)
    for relative, content in wanted.items():
        target = current / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content)
    for relative in set(actual) - set(wanted):
        (current / relative).unlink()
    for name in PUBLIC_DIRS:
        folder = current / name
        if folder.exists():
            for path in sorted(folder.rglob("*"), key=lambda p: len(p.parts), reverse=True):
                if path.is_dir() and not any(path.iterdir()):
                    path.rmdir()


def validate_descriptions(provider):
    def attributes(values, prefix):
        for name, value in values.items():
            path = prefix + "." + name
            if not value.get("description", "").strip():
                raise ValueError("Missing schema description: " + path)
            attributes(value.get("nested_type", {}).get("attributes", {}), path)

    schemas = [("provider", provider["provider"])]
    for kind in ("resource_schemas", "data_source_schemas"):
        schemas.extend(sorted(provider.get(kind, {}).items()))
    for name, value in schemas:
        block = value["block"]
        if not block.get("description", "").strip():
            raise ValueError("Missing schema description: " + name)
        attributes(block.get("attributes", {}), name)


def validate_sources(root, resources, data_sources=()):
    kinds = (("resources", "Resource", resources,
              ("resource.tf", "terraform.tf", "import.sh", "import-by-string-id.tf")),
             ("data-sources", "Data source", data_sources, ("data-source.tf", "terraform.tf")))
    for directory, label, names, examples in kinds:
        expected = {name.removeprefix("onpager_") for name in names}
        actual = {p.name.removesuffix(".md.tmpl") for p in (root / "templates" / directory).glob("*.md.tmpl")}
        if actual != expected:
            raise ValueError(label + " templates differ from schema; missing=" +
                             ",".join(sorted(expected - actual)) + "; extra=" + ",".join(sorted(actual - expected)))
        for name in names:
            for filename in examples:
                relative = Path("examples") / directory / name / filename
                if not (root / relative).is_file():
                    raise ValueError("Missing documentation example: " + relative.as_posix())


def validate_links(directory):
    for relative, content in public_files(directory).items():
        if relative.suffix != ".md":
            continue
        for raw in re.findall(r"\]\(([^)\s]+)(?:\s+[^)]*)?\)", content.decode()):
            parsed = urlsplit(raw.strip("<>"))
            if parsed.scheme or parsed.netloc or not parsed.path:
                continue
            target = ((directory / relative).parent / unquote(parsed.path)).resolve()
            if not target.is_relative_to(directory.resolve()) or not target.is_file():
                raise ValueError("Broken public documentation link in " + str(relative) + ": " + raw)


def run(command, cwd, env, log, output=None):
    result = subprocess.run(command, cwd=cwd, env=env, capture_output=True, text=True, timeout=180)
    log.write("$ " + " ".join(str(part) for part in command) + "\n")
    log.write(result.stdout + result.stderr)
    log.flush()
    if result.returncode:
        raise ValueError("Command failed: " + " ".join(str(p) for p in command) +
                         "\n" + "\n".join((result.stdout + result.stderr).splitlines()[-60:]) +
                         "\nFull log: .harness/docs.log")
    if output is not None:
        output.write_text(result.stdout)


def main():
    if len(sys.argv) != 2 or sys.argv[1] not in ("generate", "check"):
        raise ValueError("Usage: docs.py generate|check (normally task docs or task docs:check)")
    mode = sys.argv[1]
    if not shutil.which("terraform") or not shutil.which("go"):
        raise ValueError("Go and an installed Terraform CLI are required; run task doctor.")
    if not (ROOT / ".harness/terraform.tfrc").is_file():
        raise ValueError("Local plugin override is missing; run task dev first.")
    env = {k: v for k, v in os.environ.items() if not k.startswith("ONPAGER_")}
    env.update(TF_CLI_CONFIG_FILE=str(ROOT / ".harness/terraform.tfrc"), CHECKPOINT_DISABLE="1",
               TF_IN_AUTOMATION="1", GOTOOLCHAIN="local", GOCACHE=str(ROOT / ".harness/go-build"),
               GOMODCACHE=str(ROOT / ".harness/go-mod"), PYTHONDONTWRITEBYTECODE="1")
    tool = ["go", "-C", str(ROOT / "tools"), "tool", "tfplugindocs"]
    with tempfile.TemporaryDirectory(prefix="docs-", dir=ROOT / ".harness") as temporary:
        work = Path(temporary)
        # Keep subprocess TMPDIR unchanged: go-plugin creates Unix sockets there,
        # and deep checkout paths can exceed the OS socket-path limit.
        with (ROOT / ".harness/docs.log").open("w") as log:
            schema = work / "provider-schema.json"
            run(["terraform", "-chdir=" + str(ROOT / "examples/provider"), "providers", "schema", "-json"], ROOT, env, log, schema)
            document = json.loads(schema.read_text())
            provider = document["provider_schemas"][ADDRESS]
            validate_descriptions(provider)
            schema.write_text(json.dumps(generator_schema(document)))
            resources = sorted(provider.get("resource_schemas", {}))
            data_sources = sorted(provider.get("data_source_schemas", {}))
            validate_sources(ROOT, resources, data_sources)
            templates = work / "templates"
            shutil.copytree(ROOT / "templates", templates)
            stage = work / "stage"
            stage.mkdir()
            rendered = stage / "docs"
            run(tool + ["generate", "--provider-dir", str(ROOT), "--provider-name", "onpager",
                        "--providers-schema", str(schema), "--website-source-dir", str(templates.relative_to(ROOT)),
                        "--rendered-website-dir", str(rendered.relative_to(ROOT))], ROOT, env, log)
            validate_links(rendered)
            run(tool + ["validate", "--provider-dir", str(stage), "--provider-name", "onpager",
                        "--providers-schema", str(schema)], ROOT, env, log)
            example_dirs = [ROOT / "examples/provider", ROOT / "examples/team-configuration"]
            example_dirs.extend(ROOT / "examples/resources" / name for name in resources)
            example_dirs.extend(ROOT / "examples/data-sources" / name for name in data_sources)
            for example in example_dirs:
                run(["terraform", "-chdir=" + str(example), "validate", "-no-color"], ROOT, env, log)
            if mode == "check":
                differences = compare_docs(rendered, ROOT / "docs")
                if differences:
                    raise ValueError("Generated documentation differs; run task docs:\n" + "\n".join(differences))
            else:
                publish_docs(rendered, ROOT / "docs")
    print("Documentation " + ("generated" if mode == "generate" else "check passed") +
          ": " + str(len(resources)) + " resources, " + str(len(data_sources)) + " data sources, " +
          str(len(example_dirs)) + " validated examples; tfplugindocs validation passed.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError, KeyError) as error:
        sys.exit(str(error))
