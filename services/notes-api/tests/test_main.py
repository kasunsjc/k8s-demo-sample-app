import fakeredis
import pytest
from fastapi.testclient import TestClient

from app.main import create_app


@pytest.fixture
def client():
    app = create_app(lambda: fakeredis.FakeAsyncRedis(decode_responses=True))
    with TestClient(app) as c:
        yield c


def test_health_and_ready(client):
    assert client.get("/healthz").status_code == 200
    assert client.get("/readyz").json() == {"status": "ready"}


def test_note_crud(client):
    created = client.post("/notes", json={"title": "hello", "body": "k8s"})
    assert created.status_code == 201
    note_id = created.json()["id"]

    listed = client.get("/notes").json()
    assert listed["count"] == 1 and listed["notes"][0]["title"] == "hello"
    assert client.get(f"/notes/{note_id}").json()["body"] == "k8s"

    assert client.delete(f"/notes/{note_id}").status_code == 204
    assert client.get(f"/notes/{note_id}").status_code == 404


def test_note_validation(client):
    assert client.post("/notes", json={"title": ""}).status_code == 422


def test_enqueue_job(client):
    resp = client.post("/jobs", json={"type": "slow", "seconds": 1})
    assert resp.status_code == 202
    assert resp.json()["queueLength"] == 1
    assert client.get("/jobs").json()["queueLength"] == 1


def test_readiness_fails_without_redis():
    import redis.asyncio as aioredis

    app = create_app(lambda: aioredis.Redis(host="127.0.0.1", port=1, socket_connect_timeout=0.2))
    with TestClient(app) as c:
        assert c.get("/readyz").status_code == 503
        assert c.get("/notes").status_code == 503
