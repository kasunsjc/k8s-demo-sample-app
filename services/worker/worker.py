"""KubePlay worker: queue consumer (Deployment), scheduled report (CronJob), and seeder (Job)."""

import json
import logging
import os
import signal
import socket
import sys
import time
import uuid
from datetime import datetime, timezone

import redis

NOTES_KEY = "kubeplay:notes"
QUEUE_KEY = "kubeplay:jobs"
RESULTS_KEY = "kubeplay:results"
MAX_RESULTS = 50

POD_NAME = os.getenv("POD_NAME", socket.gethostname())
HEARTBEAT_FILE = os.getenv("HEARTBEAT_FILE", "/tmp/heartbeat")

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("worker")

_stopping = False


def now() -> str:
    return datetime.now(timezone.utc).isoformat()


def connect() -> redis.Redis:
    return redis.Redis(
        host=os.getenv("REDIS_HOST", "localhost"),
        port=int(os.getenv("REDIS_PORT", "6379")),
        password=os.getenv("REDIS_PASSWORD") or None,
        decode_responses=True,
        socket_connect_timeout=5,
        socket_timeout=15,
    )


def heartbeat() -> None:
    with open(HEARTBEAT_FILE, "w") as f:
        f.write(str(time.time()))


def store_result(r: redis.Redis, result: dict) -> None:
    r.lpush(RESULTS_KEY, json.dumps(result))
    r.ltrim(RESULTS_KEY, 0, MAX_RESULTS - 1)


def build_report(r: redis.Redis) -> dict:
    notes = [json.loads(n) for n in r.hvals(NOTES_KEY)]
    by_pod: dict[str, int] = {}
    for n in notes:
        by_pod[n.get("createdBy", "unknown")] = by_pod.get(n.get("createdBy", "unknown"), 0) + 1
    return {"totalNotes": len(notes), "notesByPod": by_pod, "queueLength": r.llen(QUEUE_KEY)}


def process_job(r: redis.Redis, job: dict) -> dict:
    job_type = job.get("type")
    if job_type == "report":
        output = build_report(r)
    elif job_type == "slow":
        seconds = min(max(float(job.get("payload", {}).get("seconds", 5)), 0), 60)
        time.sleep(seconds)
        output = {"sleptSeconds": seconds}
    else:
        raise ValueError(f"unknown job type: {job_type!r}")
    result = {
        "jobId": job.get("jobId", "unknown"),
        "type": job_type,
        "status": "done",
        "processedBy": POD_NAME,
        "finishedAt": now(),
        "output": output,
    }
    store_result(r, result)
    return result


def _handle_sigterm(signum, _frame) -> None:
    global _stopping
    log.info("signal %s received, finishing current job then exiting", signum)
    _stopping = True


def consume(r: redis.Redis) -> None:
    signal.signal(signal.SIGTERM, _handle_sigterm)
    signal.signal(signal.SIGINT, _handle_sigterm)
    log.info("worker %s consuming %s", POD_NAME, QUEUE_KEY)
    while not _stopping:
        heartbeat()
        try:
            item = r.blpop([QUEUE_KEY], timeout=5)
        except redis.RedisError as exc:
            log.warning("redis error: %s; retrying in 2s", exc)
            time.sleep(2)
            continue
        if not item:
            continue
        try:
            job = json.loads(item[1])
            result = process_job(r, job)
            log.info("job %s (%s) done", result["jobId"], result["type"])
        except (ValueError, json.JSONDecodeError) as exc:
            log.error("job failed: %s", exc)
            store_result(r, {"jobId": "unknown", "status": "failed", "error": str(exc), "processedBy": POD_NAME, "finishedAt": now()})
    log.info("worker stopped cleanly")


def run_report(r: redis.Redis) -> None:
    result = {
        "jobId": f"cron-{uuid.uuid4().hex[:8]}",
        "type": "scheduled-report",
        "status": "done",
        "processedBy": POD_NAME,
        "finishedAt": now(),
        "output": build_report(r),
    }
    store_result(r, result)
    log.info("scheduled report: %s", json.dumps(result["output"]))


def seed(r: redis.Redis, count: int) -> None:
    for i in range(1, count + 1):
        note = {
            "id": uuid.uuid4().hex[:12],
            "title": f"Seeded note #{i}",
            "body": "Created by a Kubernetes Job",
            "createdAt": now(),
            "createdBy": POD_NAME,
        }
        r.hset(NOTES_KEY, note["id"], json.dumps(note))
    log.info("seeded %d notes", count)


def main(argv: list[str]) -> int:
    mode = argv[1] if len(argv) > 1 else "consume"
    r = connect()
    if mode == "consume":
        consume(r)
    elif mode == "report":
        run_report(r)
    elif mode == "seed":
        seed(r, int(os.getenv("SEED_COUNT", "5")))
    else:
        log.error("unknown mode %r (expected consume|report|seed)", mode)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
