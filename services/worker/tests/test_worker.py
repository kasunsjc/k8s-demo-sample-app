import json

import fakeredis
import pytest

import worker


@pytest.fixture
def r():
    return fakeredis.FakeRedis(decode_responses=True)


def test_report_job_counts_notes(r):
    worker.seed(r, 3)
    result = worker.process_job(r, {"jobId": "j1", "type": "report"})
    assert result["output"]["totalNotes"] == 3
    stored = json.loads(r.lindex(worker.RESULTS_KEY, 0))
    assert stored["jobId"] == "j1" and stored["status"] == "done"


def test_slow_job_is_capped(r, monkeypatch):
    slept = []
    monkeypatch.setattr(worker.time, "sleep", slept.append)
    result = worker.process_job(r, {"type": "slow", "payload": {"seconds": 999}})
    assert slept == [60] and result["output"]["sleptSeconds"] == 60


def test_unknown_job_raises(r):
    with pytest.raises(ValueError):
        worker.process_job(r, {"type": "nope"})


def test_results_are_trimmed(r):
    for i in range(worker.MAX_RESULTS + 10):
        worker.store_result(r, {"jobId": str(i)})
    assert r.llen(worker.RESULTS_KEY) == worker.MAX_RESULTS


def test_scheduled_report(r):
    worker.run_report(r)
    assert json.loads(r.lindex(worker.RESULTS_KEY, 0))["type"] == "scheduled-report"
