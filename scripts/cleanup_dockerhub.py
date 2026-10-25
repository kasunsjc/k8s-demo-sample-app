import json
import os
import re
import time
from urllib.error import HTTPError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


API_BASE = "https://hub.docker.com"
REPOSITORIES = (
    "kubeplay-inspector-api",
    "kubeplay-notes-api",
    "kubeplay-worker",
    "kubeplay-ui-gateway",
)
KEEP_RELEASES = 8
VERSION_PATTERN = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
SHA_PATTERN = re.compile(r"^sha-[0-9a-fA-F]{7,40}$")


def request_json(url, method="GET", token=None, payload=None):
    headers = {"Accept": "application/json"}
    data = None
    if payload is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(payload).encode("utf-8")
    if token:
        headers["Authorization"] = f"Bearer {token}"

    for attempt in range(4):
        request = Request(url, data=data, headers=headers, method=method)
        try:
            with urlopen(request, timeout=30) as response:
                body = response.read()
                return json.loads(body) if body else {}
        except HTTPError as error:
            if error.code == 429 and attempt < 3:
                delay = int(error.headers.get("Retry-After", "2"))
                time.sleep(max(delay, 1))
                continue
            raise RuntimeError(f"Docker Hub API returned HTTP {error.code} for {method}") from error

    raise RuntimeError(f"Docker Hub API rate limit persisted for {method}")


def create_access_token(username, password):
    response = request_json(
        f"{API_BASE}/v2/auth/token",
        method="POST",
        payload={"identifier": username, "secret": password},
    )
    token = response.get("access_token")
    if not token:
        raise RuntimeError("Docker Hub did not return an access token")
    return token


def list_tags(namespace, repository, token):
    tags = []
    page = 1
    while True:
        query = urlencode({"page_size": 100, "page": page})
        url = (
            f"{API_BASE}/v2/namespaces/{quote(namespace, safe='')}"
            f"/repositories/{quote(repository, safe='')}/tags?{query}"
        )
        response = request_json(url, token=token)
        results = response.get("results")
        if not isinstance(results, list):
            raise RuntimeError(f"Docker Hub returned an invalid tag list for {repository}")
        tags.extend(item["name"] for item in results if isinstance(item.get("name"), str))
        if not response.get("next"):
            return tags
        page += 1


def version_key(tag):
    match = VERSION_PATTERN.fullmatch(tag)
    if not match:
        raise ValueError(f"Not a full SemVer tag: {tag}")
    return tuple(int(part) for part in match.groups())


def tags_to_delete(tags, keep_releases=KEEP_RELEASES):
    if keep_releases < 1:
        raise ValueError("keep_releases must be at least 1")

    unique_tags = set(tags)
    versions = sorted(
        (tag for tag in unique_tags if VERSION_PATTERN.fullmatch(tag)),
        key=version_key,
        reverse=True,
    )
    stale_versions = set(versions[keep_releases:])
    stale_builds = {tag for tag in unique_tags if SHA_PATTERN.fullmatch(tag)}
    return sorted(stale_versions | stale_builds)


def delete_tag(namespace, repository, tag, token):
    # Docker Hub's tag-delete endpoint remains on the legacy repository route.
    url = (
        f"{API_BASE}/v2/repositories/{quote(namespace, safe='')}"
        f"/{quote(repository, safe='')}/tags/{quote(tag, safe='')}/"
    )
    request_json(url, method="DELETE", token=token)


def main():
    username = os.environ.get("DOCKERHUB_USERNAME")
    password = os.environ.get("DOCKERHUB_TOKEN")
    if not username or not password:
        raise RuntimeError("DOCKERHUB_USERNAME and DOCKERHUB_TOKEN are required")

    dry_run = os.environ.get("DRY_RUN", "false").lower() == "true"
    token = create_access_token(username, password)
    for repository in REPOSITORIES:
        tags = list_tags(username, repository, token)
        deletions = tags_to_delete(tags)
        mode = "Would delete" if dry_run else "Deleting"
        print(f"{repository}: {len(tags)} tags found; {len(deletions)} selected for cleanup")
        for tag in deletions:
            print(f"{mode} {username}/{repository}:{tag}")
            if not dry_run:
                delete_tag(username, repository, tag, token)


if __name__ == "__main__":
    main()