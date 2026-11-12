#!/usr/bin/env python3
"""Run opt-in live API tests without putting credentials in command arguments."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

root = Path(__file__).resolve().parents[2]
env = os.environ.copy()
settings = root / ".harness/live-test.json"
if settings.exists():
    config = json.loads(settings.read_text())
    for variable, key in (
        ("ONPAGER_ACC_ENDPOINT", "endpoint"),
        ("ONPAGER_ACC_TOKEN", "token"),
        ("ONPAGER_ACC_USER_ID", "user_id"),
    ):
        env.setdefault(variable, config.get(key, ""))
required = ("ONPAGER_ACC_ENDPOINT", "ONPAGER_ACC_TOKEN", "ONPAGER_ACC_USER_ID")
missing = [name for name in required if not env.get(name)]
if missing:
    sys.exit("Live acceptance requires: " + ", ".join(missing))
terraform = shutil.which("terraform")
if not terraform:
    sys.exit("Terraform CLI is required for live acceptance")
env.update(TF_ACC="1", TF_ACC_TERRAFORM_PATH=terraform, TF_ACC_PROVIDER_NAMESPACE="onpager")
sys.exit(subprocess.call(
    ["go", "test", "-count=1", "-timeout=10m", "-run", "^TestAcc", "-v", "./internal/provider"],
    cwd=root, env=env,
))
