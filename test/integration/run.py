#!/usr/bin/env python3
"""Run the real sidecar against a disposable MongoDB replica set in kind."""

import base64
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
from urllib.parse import quote, quote_plus


ROOT = Path(__file__).resolve().parents[2]
PODS = ["mongo-0", "mongo-1", "mongo-2"]


class CheckFailure(Exception):
    """A safe diagnostic: never contains command output or credentials."""


def run(*args, stdin=None, allow_failure=False):
    result = subprocess.run(args, input=stdin, text=True, capture_output=True, check=False)
    if result.returncode and not allow_failure:
        # CLI stderr can echo entire Secret manifests or server error messages.
        raise CheckFailure(f"{args[0]} {args[1]} failed (exit {result.returncode}); raw output suppressed")
    return result


def kubectl_json(*args):
    return json.loads(run("kubectl", *args, "-o", "json").stdout)


def secret_ref(name, key):
    return {"name": name, "valueFrom": {"secretKeyRef": {"name": "mongo-auth", "key": key}}}


def fixture(mode, image):
    # kubectl create emits consecutive JSON objects for a multi-document YAML
    # input, whereas kubectl get returns a List. Accept either representation.
    raw = run("kubectl", "create", "--dry-run=client", "--validate=false", "-f", str(ROOT / "deployment-example.yaml"), "-o", "json").stdout
    document = {"apiVersion": "v1", "kind": "List", "items": []}
    decoder = json.JSONDecoder()
    while raw.strip():
        item, end = decoder.raw_decode(raw.lstrip())
        document["items"].extend(item["items"] if item.get("kind") == "List" else [item])
        raw = raw.lstrip()[end:]
    for item in document["items"]:
        if item["kind"] == "ConfigMap" and mode != "none":
            item["data"]["start-mongo.sh"] = (ROOT / "test/integration/start-mongo-auth.sh").read_text()
        if item["kind"] == "Service" and item["metadata"]["name"] == "mongo-cluster" and mode != "none":
            item["spec"]["publishNotReadyAddresses"] = True
        if item["kind"] != "StatefulSet":
            continue
        spec = item["spec"]["template"]["spec"]
        mongo = next(c for c in spec["containers"] if c["name"] == "mongo")
        if os.environ.get("MONGO_GLIBC_TUNABLES"):
            mongo.setdefault("env", []).append({"name": "GLIBC_TUNABLES", "value": os.environ["MONGO_GLIBC_TUNABLES"]})
        labeler = next(c for c in spec["containers"] if c["name"] == "labeler")
        labeler["image"] = image
        labeler["imagePullPolicy"] = "Never"
        labeler["env"] = [v for v in labeler["env"] if v["name"] not in {"MONGO_ADDRESS", "DEBUG"}]
        labeler["env"] += [{"name": "DEBUG", "value": "true"}, {"name": "MONGODB_LOG_ALL", "value": "debug"}]
        if mode == "uri":
            labeler["env"].append(secret_ref("MONGO_ADDRESS", "uri"))
        else:
            labeler["env"].append({"name": "MONGO_ADDRESS", "value": "localhost:27017"})
        if mode in {"env", "invalid"}:
            labeler["env"] += [secret_ref("MONGO_USERNAME", "username"),
                               secret_ref("MONGO_PASSWORD", "bad-password" if mode == "invalid" else "password"),
                               {"name": "MONGO_AUTH_SOURCE", "value": "admin"}]
        if mode == "none":
            continue
        # All members must start before the first member can complete auth
        # bootstrap. Publish their DNS names before readiness for the same reason.
        item["spec"]["podManagementPolicy"] = "Parallel"
        mongo.setdefault("env", []).extend([secret_ref("MONGO_TEST_USERNAME", "username"), secret_ref("MONGO_TEST_PASSWORD", "password")])
        mongo["volumeMounts"].append({"name": "mongo-auth", "mountPath": "/run/mongo-auth", "readOnly": True})
        mongo["readinessProbe"] = {"exec": {"command": ["test", "-f", "/tmp/mongo-auth-ready"]}, "periodSeconds": 2}
        spec["volumes"].append({"name": "mongo-auth", "secret": {"secretName": "mongo-auth", "defaultMode": 256,
                                                                    "items": [{"key": "keyfile", "path": "keyfile"}]}})
    return document


def create_credentials():
    values = {"username": "sidecar-" + secrets.token_hex(12) + "@test",
              "password": secrets.token_urlsafe(24) + ":@/%+?&=",
              "bad-password": secrets.token_urlsafe(24) + ":@/%+?&=",
              "keyfile": base64.b64encode(secrets.token_bytes(512)).decode()}
    values["uri"] = "mongodb://" + quote(values["username"], safe="") + ":" + quote(values["password"], safe="") + "@localhost:27017/?authSource=admin"
    document = {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "mongo-auth"}, "type": "Opaque",
                "data": {k: base64.b64encode(v.encode()).decode() for k, v in values.items()}}
    run("kubectl", "create", "-f", "-", stdin=json.dumps(document))
    needles = set()
    for value in values.values():
        needles.update([value, quote(value, safe=""), quote_plus(value, safe="")])
    return needles, values["username"]


def check_logs(needles, since=None):
    logs = {}
    for pod in PODS:
        args = ["kubectl", "logs", pod, "-c", "labeler"]
        if since:
            args.append("--since-time=" + since)
        result = run(*args, allow_failure=True)
        if result.returncode:
            raise CheckFailure("unable to read sidecar logs")
        if any(secret in result.stdout for secret in needles):
            raise CheckFailure("credential detected in sidecar logs; log content suppressed")
        logs[pod] = result.stdout
    return logs


def check_labels():
    pods = kubectl_json("get", "pods", "-l", "role=mongo")["items"]
    labels = {p["metadata"]["name"]: p["metadata"].get("labels", {}).get("primary") for p in pods}
    return pods, labels


def verify_routing(needles):
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        pods, labels = check_labels()
        check_logs(needles)
        if len(labels) == 3 and list(labels.values()).count("true") == 1 and list(labels.values()).count("false") == 2:
            primary = next(p for p in pods if labels[p["metadata"]["name"]] == "true")
            slices = kubectl_json("get", "endpointslices", "-l", "kubernetes.io/service-name=mongo")
            addresses = {address for s in slices["items"] for ep in s.get("endpoints", [])
                         if ep.get("conditions", {}).get("ready", True) for address in ep["addresses"]}
            if addresses == {primary["status"]["podIP"]}:
                print("PASS: one primary, two secondary labels, and Service routes to the primary", flush=True)
                return
        time.sleep(2)
    raise CheckFailure("labels or Service endpoints did not converge")


def authentication_rejected(username, since):
    for pod in PODS:
        # MongoDB's structured rejection event identifies the tested account.
        # Inspect it privately: server logs include usernames and must never be
        # sent to diagnostics, even when this assertion fails.
        text = run("kubectl", "logs", pod, "-c", "mongo", "--since-time=" + since).stdout
        rejected = False
        for line in text.splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            attrs = event.get("attr", {})
            if event.get("id") == 5286307 and attrs.get("user") == username and attrs.get("result") == 18:
                rejected = True
        if not rejected:
            return False
    return True


def verify_invalid_password(needles, username):
    # Observe fresh failures after bootstrap has authenticated the correct user
    # on every member. The driver can keep its pool paused after an auth failure;
    # these retry errors do not expose the original authentication error type.
    since = datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")
    start = time.monotonic()
    deadline = start + 90
    while time.monotonic() < deadline:
        _, labels = check_labels()
        if len(labels) != 3 or any(value is not None for value in labels.values()):
            raise CheckFailure("invalid credentials caused a label change")
        all_logs = check_logs(needles)
        if any("Patching pod" in text or "primary detected" in text for text in all_logs.values()):
            raise CheckFailure("invalid credentials reached pod patching")
        logs = check_logs(needles, since)
        rejected = authentication_rejected(username, since)
        retrying = all(text.count("authentication_failed") + text.count("connection_pool_unavailable") >= 2 for text in logs.values())
        if time.monotonic() - start >= 15 and rejected and retrying:
            print("PASS: every sidecar rejected the password and kept failing without patching labels", flush=True)
            return
        time.sleep(2)
    raise CheckFailure("sidecars did not report authentication rejection followed by failed retries")


def diagnostics(needles):
    # MongoDB logs can include usernames; never print them. Sidecar output is
    # useful only after it has passed the same credential scan as successful runs.
    print("Diagnostics: pod state and credential-checked sidecar logs", flush=True)
    try:
        pods, labels = check_labels()
        for pod in pods:
            print(pod["metadata"]["name"], pod.get("status", {}).get("phase"), labels[pod["metadata"]["name"]])
        for name, text in check_logs(needles).items():
            print(name + ":\n" + "\n".join(text.splitlines()[-20:]))
    except CheckFailure as error:
        print(str(error), flush=True)


def main():
    mode = os.environ.get("MONGO_AUTH_MODE", "none")
    if mode not in {"none", "env", "uri", "invalid"}:
        raise CheckFailure("MONGO_AUTH_MODE must be none, env, uri, or invalid")
    for tool in ("docker", "kind", "kubectl"):
        if not shutil.which(tool):
            raise CheckFailure(f"missing prerequisite: {tool}")
    cluster = os.environ.get("CLUSTER_NAME", "kind-mongo-labeler")
    image = os.environ.get("LABELER_IMAGE", "mongo-labeler:local")
    timeout = os.environ.get("TIMEOUT", "240s")
    keep = os.environ.get("KEEP_CLUSTER", "false") == "true"
    created = False
    needles = set()
    username = None
    os.environ["DOCKER_BUILDKIT"] = "1"
    with tempfile.TemporaryDirectory(prefix="mongo-labeler-integration-") as temp:
        if not os.environ.get("KUBECONFIG"):
            os.environ["KUBECONFIG"] = str(Path(temp) / "kubeconfig")
        run("docker", "info")
        if cluster in run("kind", "get", "clusters").stdout.splitlines():
            raise CheckFailure("the named kind cluster already exists; choose a unique CLUSTER_NAME")
        if os.environ.get("USE_PREBUILT_IMAGE", "false") == "true":
            run("docker", "image", "inspect", image)
        else:
            run("docker", "buildx", "version")
            print("Building the sidecar image from this checkout", flush=True)
            run("docker", "build", "-t", image, str(ROOT))
        try:
            print(f"Creating isolated cluster for {mode} authentication scenario", flush=True)
            # The name was confirmed unused; clean up a partially created cluster
            # too if kind fails after creating its Docker container.
            created = True
            run("kind", "create", "cluster", "--name", cluster)
            run("kind", "load", "docker-image", image, "--name", cluster)
            if mode != "none":
                needles, username = create_credentials()
            run("kubectl", "apply", "-f", "-", stdin=json.dumps(fixture(mode, image)))
            run("kubectl", "rollout", "status", "statefulset/mongo", "--timeout=" + timeout)
            if mode != "none":
                # hello and ping are intentionally available without auth. Use a
                # protected command to prove access control really is enforced.
                run("kubectl", "exec", "mongo-0", "-c", "mongo", "--", "mongosh", "--quiet", "--eval",
                    "try { db.adminCommand({getCmdLineOpts: 1}); quit(1); } catch (error) { quit(error.code === 13 ? 0 : 1); }")
                print("PASS: MongoDB rejects an unauthenticated protected command", flush=True)
            if mode == "invalid":
                verify_invalid_password(needles, username)
            else:
                verify_routing(needles)
            check_logs(needles)
            print("PASS: sidecar logs contain no credentials", flush=True)
        except CheckFailure:
            if created:
                diagnostics(needles)
            raise
        finally:
            if created and not keep:
                run("kind", "delete", "cluster", "--name", cluster, allow_failure=True)
            elif created:
                print(f"Kept cluster {cluster}; recover access with kind export kubeconfig --name {cluster}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("Integration test interrupted", file=sys.stderr)
        sys.exit(130)
    except CheckFailure as failure:
        print("FAIL: " + str(failure), file=sys.stderr)
        sys.exit(1)
