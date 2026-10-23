"""Verify the installed real SQLBot image, then inject drift in a disposable container.

Run only in a disposable container with no application data or network mounted.
The mutation is intentional acceptance fault injection, never a repair strategy.
"""
import json
import runpy
from pathlib import Path


def main():
    installer = runpy.run_path("/tmp/agentsql-sqlbot/install.py", run_name="acceptance_installer")
    app = Path("/opt/sqlbot/app")
    baseline = installer["verify_install"](app)
    assert baseline["adapter_version"] == 2 and baseline["checked_files"] == 8
    target = app / "main.py"
    target.write_bytes(target.read_bytes() + b"\n# acceptance source drift\n")
    try:
        installer["verify_install"](app)
    except ValueError as error:
        assert "drift" in str(error) and "main.py" in str(error)
    else:
        raise AssertionError("Installed source drift was accepted")
    original = (app / "main.py.agentsql-backup").read_text()
    try:
        installer["patch_support"]("main.py", original + "\n# unsupported upstream source\n")
    except ValueError as error:
        assert "source hash" in str(error)
    else:
        raise AssertionError("Unsupported upstream source was accepted")
    print(json.dumps({"baseline": baseline, "installed_drift_rejected": True,
                      "unknown_upstream_source_rejected": True}))


if __name__ == "__main__":
    main()
