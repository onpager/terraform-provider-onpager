#!/usr/bin/env python3
"""Check the public Git candidate tree without staging or initializing this checkout."""
import argparse
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
import tempfile
from urllib.parse import unquote, urlsplit


ROOT = Path(__file__).resolve().parents[2]
# Keep this directory-level policy broad enough for normal provider development.
PUBLIC_DIRECTORIES = {".github", "assets", "cmd", "docs", "examples", "internal",
                      "pkg", "scripts", "templates", "testdata", "tests", "tools"}
ROOT_SUFFIXES = {".go", ".mod", ".sum", ".md", ".rst", ".txt", ".json", ".yaml",
                 ".yml", ".toml", ".hcl", ".sh", ".py"}
ROOT_FILES = {".gitignore", ".gitattributes", ".editorconfig", ".env.example",
              ".envrc.example", "LICENSE", "NOTICE", "COPYING", "Makefile", "Dockerfile"}
PRIVATE_PROBES = (
    ".harness/internal-docs/notes.md", ".harness/credentials.env", "bin/provider", "dist/provider.zip",
    ".local/docs/api-gaps.md", ".local/docs/superpowers/plan.md", ".pass", ".test_integration_credentials",
    ".codex/config.toml", ".superpowers/plan.md", "AGENTS.md", ".idea/workspace.xml",
    ".vscode/settings.json", "docs/superpowers/plan.md", "docs/development/setup.md",
    "docs/api-gaps.md", "docs/releasing.md", "private.env", ".env", ".env.local",
    "examples/demo/.terraform/providers/cache", "examples/demo/terraform.tfstate",
    "examples/demo/terraform.tfstate.backup", "examples/demo/saved.tfplan", "examples/demo/saved.plan",
    "examples/demo/terraform.tfvars", "examples/demo/values.tfvars.json", "examples/demo/crash.log",
    "examples/demo/crash.123.log", "scripts/harness/__pycache__/check.pyc", "coverage.out",
    "internal/client/client.test", "internal/client/cpu.prof",
)
PUBLIC_PROBES = (
    "main.go", "go.mod", "go.sum", "tools/go.mod", "tools/go.sum", "README.md", "docs/index.md",
    "templates/resources/team.md.tmpl", "examples/resources/onpager_team/resource.tf",
    "examples/resources/onpager_team/import.sh", "examples/resources/onpager_team/import-by-string-id.tf",
    "examples/demo/terraform.tfvars.example", "examples/demo/values.tfvars.json.example",
    ".env.example", ".terraform.lock.hcl", ".github/workflows/check.yml",
)
NARRATIVE = re.compile(r"(?<![a-z])compan(?:y|ies)(?![a-z])|компани", re.IGNORECASE)
LOCAL_REFERENCE = re.compile(r"file://|(?:/home/|/Users/|/workspace/|/tmp/)[^\s`<>]*|"
                             r"[A-Za-z]:[\\/]Users[\\/]|\.\./onpager(?:[/\s`]|$)")
INLINE_LINK = re.compile(r"\]\(\s*(<[^>\n]+>|[^\s)]+)")
REFERENCE_LINK = re.compile(r"^[ \t]{0,3}\[[^\]]+\]:[ \t]*(<[^>\n]+>|\S+)", re.MULTILINE)


def git_environment():
    # Local/global excludes and inherited Git overrides must not hide publication inputs.
    environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    environment.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_TERMINAL_PROMPT="0", GIT_OPTIONAL_LOCKS="0")
    return environment


def run_git(arguments, root, data=None, allowed=(0,)):
    result = subprocess.run(["git", *arguments], cwd=root, env=git_environment(),
                            input=data, capture_output=True, timeout=30)
    if result.returncode not in allowed:
        # Do not echo process output: a failed command must never disclose local file contents.
        raise ValueError("Git publication inventory failed; check Git availability and repository metadata.")
    return result.stdout


def split_paths(data):
    return {os.fsdecode(value) for value in data.split(b"\0") if value}


def ignored_paths(git, root, paths):
    if not paths:
        return set()
    data = b"\0".join(os.fsencode(path) for path in paths) + b"\0"
    return split_paths(run_git([*git, "check-ignore", "--no-index", "--stdin", "-z"],
                               root, data, allowed=(0, 1)))


def inventory(root):
    """Use an empty temporary index; also include already tracked files if Git exists."""
    with tempfile.TemporaryDirectory(prefix="onpager-publication-") as temporary:
        metadata = Path(temporary) / "git"
        run_git(["init", "--quiet", "--bare", "--template=", str(metadata)], root)
        git = ["--git-dir=" + str(metadata), "--work-tree=" + str(root),
               "-c", "core.excludesFile=" + os.devnull]
        candidates = split_paths(run_git([*git, "ls-files", "--others", "--exclude-standard", "-z"], root))
        tracked = set()
        if (root / ".git").exists():
            top = os.fsdecode(run_git(["-C", str(root), "rev-parse", "--show-toplevel"], root)).strip()
            if Path(top).resolve() != root:
                raise ValueError("Publication check must run at the repository root.")
            tracked = split_paths(run_git(["-C", str(root), "ls-files", "--cached", "-z"], root))
            candidates.update(tracked)
        errors = ["Ignore rule missing for private path: " + path
                  for path in sorted(set(PRIVATE_PROBES) - ignored_paths(git, root, PRIVATE_PROBES))]
        errors.extend("Public source path is ignored: " + path
                      for path in sorted(ignored_paths(git, root, PUBLIC_PROBES)))
        errors.extend("Tracked ignored file: " + path
                      for path in sorted(ignored_paths(git, root, tracked)))
    return sorted(candidates), errors


def public_text(path):
    return (path.parts[0] in {"docs", "templates", "examples"}
            or len(path.parts) == 1 and path.suffix.lower() in {".md", ".rst", ".txt"})


def allowed_path(path):
    if len(path.parts) > 1:
        return path.parts[0] in PUBLIC_DIRECTORIES
    return (path.name in ROOT_FILES or path.suffix.lower() in ROOT_SUFFIXES
            or path.name.endswith((".tfvars.example", ".tfvars.json.example", ".env.example")))


def validate_links(root, relative, text, candidates):
    # Template links are relative to the resulting Registry page, not its source directory.
    location = relative
    if relative.parts[0] == "templates" and relative.name.endswith(".md.tmpl"):
        location = PurePosixPath("docs", *relative.parts[1:]).with_suffix("")
    errors = []
    for match in (*INLINE_LINK.finditer(text), *REFERENCE_LINK.finditer(text)):
        raw = match.group(1).strip("<>")
        if "{{" in raw:
            continue
        parsed = urlsplit(raw)
        if parsed.scheme or parsed.netloc or not parsed.path:
            continue
        target = (root / location.parent / unquote(parsed.path)).resolve()
        try:
            name = target.relative_to(root).as_posix()
        except ValueError:
            name = ""
        if not name or (name not in candidates and not any(p.startswith(name + "/") for p in candidates)):
            errors.append("Unpublished local link in " + relative.as_posix() +
                          ":" + str(text.count("\n", 0, match.start()) + 1))
    return errors


def check_files(root, candidates):
    errors = []
    paths = set(candidates)
    for name in candidates:
        relative = PurePosixPath(name)
        path = root / name
        if not allowed_path(relative):
            errors.append("Unexpected publication path: " + name)
            continue
        if path.is_symlink():
            target = path.resolve()
            try:
                destination = target.relative_to(root).as_posix()
            except ValueError:
                destination = ""
            if destination not in paths:
                errors.append("Symlink targets an unpublished file: " + name)
                continue
        if not path.is_file():
            errors.append("Publication file is missing or unsupported: " + name)
            continue
        if not public_text(relative):
            continue
        if NARRATIVE.search(name):
            errors.append("Disallowed narrative term in public path: " + name)
        # Assets can be public without being prose. Text errors report locations, never contents.
        if path.suffix.lower() not in {".md", ".tmpl", ".tf", ".sh", ".txt", ".rst", ".example"}:
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeError:
            errors.append("Public text must use UTF-8: " + name)
            continue
        if NARRATIVE.search(text):
            errors.append("Disallowed narrative term in public text: " + name)
        if LOCAL_REFERENCE.search(text):
            errors.append("Local filesystem reference in public text: " + name)
        if relative.name.endswith((".md", ".md.tmpl")):
            errors.extend(validate_links(root, relative, text, paths))
    return errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("check", "inventory"))
    parser.add_argument("--root", type=Path, default=ROOT)
    options = parser.parse_args()
    root = options.root.resolve()
    candidates, errors = inventory(root)
    if options.command == "inventory":
        # One relative path per line, suitable for an ignored review manifest.
        print("\n".join(candidates))
        return
    errors.extend(check_files(root, candidates))
    if errors:
        raise ValueError("Repository publication check failed:\n" + "\n".join(errors))
    print("Repository publication check passed: " + str(len(candidates)) + " Git-includable files.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.TimeoutExpired) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
