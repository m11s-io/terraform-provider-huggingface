#!/usr/bin/env python3
"""Opt-in disposable HF API contract probe; credentials come only from env."""
import json
import os
import pathlib
import urllib.error
import urllib.request
import uuid


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def main():
    token = os.environ["HF_TOKEN"]
    namespace = os.environ["HF_TEST_NAMESPACE"]
    if not token or not namespace or "/" in namespace:
        raise SystemExit("HF_TOKEN and a dedicated HF_TEST_NAMESPACE are required")
    opener = urllib.request.build_opener(NoRedirect)
    events = []

    def request(method, path, body=None):
        req = urllib.request.Request(
            "https://huggingface.co" + path,
            method=method,
            data=None if body is None else json.dumps(body).encode(),
            headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
        )
        try:
            with opener.open(req, timeout=30) as response:
                status, raw = response.status, response.read(2_000_000)
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read(2_000_000)
        try:
            data = json.loads(raw)
        except ValueError:
            data = {"text": raw.decode(errors="replace")}
        if isinstance(data, list):
            for item in data:
                if isinstance(item, dict):
                    item.pop("configuredBy", None)
        elif isinstance(data, dict):
            data.pop("configuredBy", None)
        events.append({"method": method, "path": path, "request": body, "status": status, "response": data})
        print(method, path, status, flush=True)
        return status, data

    # No request retry, including mutations. Names and claims are disposable.
    name = "terraform-provider-probe-" + uuid.uuid4().hex[:12]
    repo = namespace + "/" + name
    base = "/api/models/" + repo
    publishers = base + "/settings/trusted-publishers"
    cleanup_required = False
    try:
        cleanup_required = True  # also clean up a create whose response was lost
        status, _ = request("POST", "/api/repos/create", {"name": name, "organization": namespace, "type": "model", "private": True})
        assert status == 200, "Disposable repository create failed"
        status, values = request("GET", publishers)
        assert status == 200 and values == [], "Machine-token publisher listing failed"
        ids = []
        for suffix in ("first", "second"):
            payload = {
                "issuerUrl": "https://token.actions.githubusercontent.com",
                "claims": {
                    "repository": {"kind": "eq", "value": "m11s-io/docker-images"},
                    "ref": {"kind": "eq", "value": "refs/heads/" + name + "-" + suffix},
                    "workflow_ref": {"kind": "prefix", "value": "m11s-io/docker-images/.github/workflows/disposable-provider-probe.yaml@"},
                },
            }
            status, value = request("POST", publishers, payload)
            assert status == 200 and value.get("_id"), "Publisher create failed"
            assert value["claims"] == payload["claims"], "Publisher claims changed"
            ids.append(value["_id"])
        status, values = request("GET", publishers)
        assert status == 200 and {v["_id"] for v in values} == set(ids)
        assert request("DELETE", publishers + "/" + ids[0])[0] == 200
        status, values = request("GET", publishers)
        assert status == 200 and [v["_id"] for v in values] == [ids[1]], "Delete lost unrelated publisher"
        assert request("DELETE", publishers + "/" + ids[1])[0] == 200
        assert request("GET", publishers) == (200, [])
    finally:
        if cleanup_required:
            status, _ = request("DELETE", "/api/repos/delete", {"name": name, "organization": namespace, "type": "model"})
            assert status in (200, 404), "Cleanup failed: remove the printed disposable repository"
            assert request("GET", base)[0] == 404, "Disposable repository still exists"
        # Never persist request headers or a token, including in error responses.
        output = json.dumps(events, indent=2).replace(token, "[REDACTED]")
        pathlib.Path("probe-events.json").write_text(output + "\n")


if __name__ == "__main__":
    main()
