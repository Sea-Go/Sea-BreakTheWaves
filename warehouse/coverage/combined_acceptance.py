"""One live Publisher -> Verifier -> H10 run with isolated RTW/DC/PG/CH/S3."""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import urllib.parse
import urllib.error
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
sys.path.insert(0, str(HERE))
from acceptance import build_dwd, preserve_ref_artifacts, sha  # noqa: E402
sys.path.insert(0, str(HERE.parent / "scripts"))
from local_server import free_port, start as start_clickhouse  # noqa: E402
from local_s3 import start as start_s3  # noqa: E402
from engine import ClickHouse  # noqa: E402


def go_test(output: Path, env: dict[str, str], name: str) -> None:
    with output.open("wb") as log:
        result = subprocess.run(["go", "test", "-mod=readonly", "-race", "-count=1", "-v",
                                 "-run", f"^{name}$", "./warehouse/coverage"],
                                cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
    if result.returncode:
        raise RuntimeError(f"{name} failed; inspect {output}:\n{output.read_text()[-5000:]}")


def preserve_candidate(output: Path, label: str, report: dict, prefix: str,
                       generations: dict[str, str]) -> None:
    directory = output / "artifacts" / label / "candidates"
    directory.mkdir(parents=True, exist_ok=False)
    for key, generation in generations.items():
        digest = report[key]
        url = f"{prefix}/warehouse-coverage/{generation}/covered-baseline/{digest}.json"
        with urllib.request.urlopen(url, timeout=30) as response:
            body = response.read()
        if sha(body) != digest:
            raise AssertionError(f"{label} H10 candidate hash changed: {key}")
        (directory / f"{key}.json").write_bytes(body)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--ready", required=True)
    parser.add_argument("--pg-dsn", required=True)
    parser.add_argument("--postgres-bin", required=True)
    args = parser.parse_args()
    runtime, output = Path(args.runtime).resolve(), Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=False)
    for name in ("clickhouse", "weed", ".venv/bin/dbt"):
        if not (runtime / name).exists():
            raise RuntimeError(f"locked runtime dependency missing: {name}")
    ready = json.loads(Path(args.ready).read_text())
    parsed = urllib.parse.urlsplit(args.pg_dsn)
    if parsed.path != "/postgres":
        raise RuntimeError("isolated PostgreSQL source database must be postgres")
    def dsn(database: str) -> str:
        return urllib.parse.urlunsplit(parsed._replace(path="/" + database))
    for database in ("coverage_real_model", "coverage_two_dc", "coverage_two_model"):
        subprocess.run([str(Path(args.postgres_bin) / "psql"), "-X", "-q", "-d", args.pg_dsn,
                        "-c", f"CREATE DATABASE {database}"], check=True, capture_output=True)
    ch_proc = s3_proc = two_dc_proc = None
    try:
        ch_proc, endpoint = start_clickhouse(runtime / "clickhouse", output / "clickhouse")
        s3_proc, prefix = start_s3(runtime / "weed", output / "seaweed")
        env = os.environ.copy()
        env.update(COVERAGE_JOIN_REAL_WAREHOUSE_DSN=args.pg_dsn,
                   COVERAGE_JOIN_REAL_MODEL_DSN=dsn("coverage_real_model"),
                   COVERAGE_JOIN_REAL_DC_URL=ready["dc_url"],
                   COVERAGE_JOIN_REAL_DC_TOKEN=ready["dc_token"],
                   COVERAGE_JOIN_REAL_AUTHORITY_URL=ready["authority_url"],
                   COVERAGE_JOIN_REAL_AUTHORITY_TOKEN=ready["authority_token"],
                   COVERAGE_JOIN_S3_PREFIX=prefix,
                   COVERAGE_JOIN_REAL_REPORT=str(output / "real-joined-ref.json"),
                   COVERAGE_JOIN_REAL_ODS_OUTPUT=str(output / "real-ods.jsonl"),
                   GOFLAGS="-p=2", GOMAXPROCS="2")
        go_test(output / "real-combined-test.log", env, "TestCombinedRealRTWPublisherVerifierH10")

        two_port = free_port()
        two_url = f"http://127.0.0.1:{two_port}"
        two_env = env.copy()
        two_env["DATABASE_URL"] = dsn("coverage_two_dc")
        dc_binary = output.parent / "dc-platform"
        with (output / "two-dc.log").open("wb") as log:
            two_dc_proc = subprocess.Popen([str(dc_binary), "-listen", f"127.0.0.1:{two_port}", "-migrate"],
                                           env=two_env, stdout=log, stderr=subprocess.STDOUT)
        for _ in range(100):
            if two_dc_proc.poll() is not None:
                raise RuntimeError("second isolated DC exited; inspect two-dc.log")
            try:
                request = urllib.request.Request(two_url + "/v1/events/rtw.community.favorite/readiness",
                                                 headers={"Authorization": "Bearer " + ready["dc_token"]})
                urllib.request.urlopen(request, timeout=1).close()
            except urllib.error.HTTPError as error:
                if error.code == 404:
                    break
            except (OSError, TimeoutError):
                pass
            import time
            time.sleep(0.2)
        else:
            raise RuntimeError("second isolated DC did not become ready")
        env.update(COVERAGE_JOIN_TWO_WAREHOUSE_DSN=dsn("coverage_two_dc"),
                   COVERAGE_JOIN_TWO_MODEL_DSN=dsn("coverage_two_model"),
                   COVERAGE_JOIN_TWO_DC_URL=two_url,
                   COVERAGE_JOIN_TWO_DC_TOKEN=ready["dc_token"],
                   COVERAGE_JOIN_TWO_REPORT=str(output / "two-joined-ref.json"),
                   COVERAGE_JOIN_TWO_ODS_OUTPUT=str(output / "two-ods.jsonl"))
        go_test(output / "two-combined-test.log", env, "TestCombinedTwoSubjectsPublisherVerifierH10")

        ch = ClickHouse(endpoint)
        real_dwd = build_dwd(ch, endpoint, prefix, runtime, output, "real",
                             [("1001", "assert", 1), ("1001", "retract", 2)])
        two_dwd = build_dwd(ch, endpoint, prefix, runtime, output, "two",
                            [("1001", "assert", 1), ("1002", "assert", 2), ("1001", "retract", 3)])
        real_report = json.loads((output / "real-joined-ref.json").read_text())
        two_report = json.loads((output / "two-joined-ref.json").read_text())
        preserve_ref_artifacts(output, "real", {k: v for k, v in real_report.items() if k.startswith(("G", "U"))})
        preserve_ref_artifacts(output, "two", {k: v for k, v in two_report.items() if k.startswith(("G", "U"))})
        preserve_candidate(output, "real", real_report, prefix, {
            "candidate_w1_sha256": "combined_real_candidate_w1",
            "candidate_w1_after_retract_sha256": "combined_real_candidate_w1_after_retract",
            "candidate_w2_sha256": "combined_real_candidate_w2"})
        preserve_candidate(output, "two", two_report, prefix, {
            "candidate_w1_sha256": "combined_two_candidate_w1",
            "candidate_w1_after_tail_sha256": "combined_two_candidate_w1_after_tail",
            "candidate_u1_w3_sha256": "combined_two_candidate_u1_w3",
            "candidate_u2_w3_sha256": "combined_two_candidate_u2_w3",
            "candidate_u3_w3_sha256": "combined_two_candidate_u3_w3"})
        if [two_report[key]["event_count"] for key in ("U1W3", "U2W3", "U3W3")] != [2, 1, 0]:
            raise AssertionError("two-subject publisher refs changed")
        result = {"status": "passed", "activation": "default_off", "accept_v2": "not_called",
                  "real": {"evidence_level": "L2_same_run_real_RTW_DC_PG_CH_S3", "global_W": [1, 2], **real_dwd},
                  "two": {"evidence_level": "L2_same_run_fixture_RTW_authority_real_DC_PG_CH_S3",
                          "global_W": [1, 3], "u1_offsets": [1, 3], "u2_offsets": [2], "u3_offsets": [], **two_dwd},
                  "model_or_recommendation_effect": None, "exposure_or_label": None}
        (output / "report.json").write_text(json.dumps(result, sort_keys=True, indent=2) + "\n")
        print(json.dumps(result, sort_keys=True, indent=2), flush=True)
    finally:
        if two_dc_proc is not None:
            two_dc_proc.terminate()
            try:
                two_dc_proc.wait(timeout=15)
            except subprocess.TimeoutExpired:
                two_dc_proc.kill()
                two_dc_proc.wait(timeout=10)
        for proc in (s3_proc, ch_proc):
            if proc is not None:
                proc.terminate()
                try:
                    proc.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=10)


if __name__ == "__main__":
    main()
