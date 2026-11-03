#!/usr/bin/env python3
"""Test the deployed adapter expressions with promtool (requires PyYAML)."""
import pathlib
import subprocess
import tempfile

import yaml

here = pathlib.Path(__file__).resolve().parent
config = yaml.safe_load((here.parent / "config.yaml").read_text())
resources = yaml.safe_load(config["data"]["config.yaml"])["resourceRules"]
rules = []
for resource in ("cpu", "memory"):
    expression = resources[resource]["containerQuery"]
    expression = expression.replace("<<.GroupBy>>", "namespace,pod,container")
    expression = expression.replace("<<.LabelMatchers>>", 'namespace="test"')
    rules.append({"record": "adapter_" + resource, "expr": expression})

with tempfile.TemporaryDirectory() as temporary:
    directory = pathlib.Path(temporary)
    (directory / "rules.yaml").write_text(yaml.safe_dump({
        "groups": [{"name": "adapter", "rules": rules}]
    }))
    (directory / "fallback.test.yaml").write_text((here / "fallback.test.yaml").read_text())
    subprocess.run(["promtool", "test", "rules", "fallback.test.yaml"], cwd=directory, check=True)
