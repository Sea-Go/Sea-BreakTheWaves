"""The sdist and both wheel routes must carry the one schema source exactly."""

from pathlib import Path
import shutil
import subprocess
import tarfile
import zipfile

import pytest


def _tar_schemas(path: Path) -> dict[str, bytes]:
    result = {}
    with tarfile.open(path) as archive:
        for member in archive.getmembers():
            marker = "/contracts/jsonschema/"
            if marker in member.name and member.isfile():
                result[member.name.split(marker, 1)[1]] = archive.extractfile(member).read()
        assert any(member.name.endswith("/hatch_build.py") for member in archive.getmembers())
    return result


def _wheel_schemas(path: Path) -> dict[str, bytes]:
    with zipfile.ZipFile(path) as archive:
        return {name.split("sea_training/schemas/", 1)[1]: archive.read(name)
                for name in archive.namelist()
                if name.startswith("sea_training/schemas/") and not name.endswith("/")}


def test_default_sdist_wheel_and_direct_wheel_have_identical_authoritative_schemas(tmp_path):
    uv = shutil.which("uv")
    if uv is None:
        pytest.skip("uv CLI required for packaging regression")
    root = Path(__file__).resolve().parents[1]
    authoritative = root.parent / "contracts" / "jsonschema"
    expected = {path.name: path.read_bytes() for path in authoritative.iterdir() if path.is_file()}
    assert "recommend-engagement.columns.v1.json" in expected
    assert "search-qrel.columns.v1.json" in expected
    outputs = {name: tmp_path / name for name in ("default", "direct")}
    subprocess.run([uv, "build", "--out-dir", str(outputs["default"])],
                   cwd=root, check=True, capture_output=True, timeout=120)
    subprocess.run([uv, "build", "--wheel", "--out-dir", str(outputs["direct"])],
                   cwd=root, check=True, capture_output=True, timeout=120)
    sdist = next(outputs["default"].glob("*.tar.gz"))
    default_wheel = next(outputs["default"].glob("*.whl"))
    direct_wheel = next(outputs["direct"].glob("*.whl"))
    assert _tar_schemas(sdist) == expected
    assert _wheel_schemas(default_wheel) == expected
    assert _wheel_schemas(direct_wheel) == expected
