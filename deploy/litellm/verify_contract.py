"""Exercise the real LiteLLM HTTP proxy and both native adapters without paid calls.

Run in the gateway image with --network none. All provider traffic goes to a
loopback HTTP stub. This is a protocol check, not vendor availability validation.
"""
import importlib.metadata
import json
import os
import socket
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.error import URLError
from urllib.parse import quote
from urllib.request import Request, urlopen

import yaml
from litellm.llms.minimax.videos.transformation import MinimaxVideoConfig
from litellm.llms.volcengine.videos.transformation import VolcEngineVideoConfig

KEY = "sk-local-contract-check-only"
VIDEO_BYTES = b"contract-test-video-bytes"
SEEDANCE = "volcengine/doubao-seedance-2-0-260128"
H3 = "minimax/MiniMax-H3"
H3_MAX = "minimax/MiniMax-H3-Max"


class Provider(BaseHTTPRequestHandler):
    tasks = {}
    creates = []
    status = "succeeded"

    def log_message(self, *_args):
        pass

    def respond(self, payload, status=200):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        assert self.headers.get("Authorization") == "Bearer vendor-stub-key"
        assert "seconds" not in body and "size" not in body, body
        assert body["duration"] == 5, body
        assert body["content"][0] == {"type": "text", "text": "Ocean waves"}, body
        task_id = f"task-{len(self.tasks) + 1}"
        self.tasks[task_id] = body
        self.creates.append((self.path, body))
        if self.path == "/ark/api/v3/contents/generations/tasks":
            self.respond({"id": task_id})
        elif self.path == "/minimax/v2/video_generation":
            self.respond({"task_id": task_id})
        else:
            self.respond({"error": "unexpected native path"}, 404)

    def do_GET(self):
        if self.path == "/video.mp4":
            self.send_response(200)
            self.send_header("Content-Type", "video/mp4")
            self.send_header("Content-Length", str(len(VIDEO_BYTES)))
            self.end_headers()
            self.wfile.write(VIDEO_BYTES)
            return
        task_id = self.path.rsplit("/", 1)[-1]
        body = self.tasks.get(task_id)
        if body is None:
            self.respond({"error": "unknown task"}, 404)
            return
        native = dict(id=task_id, model=body["model"], status=self.status,
                      duration=body["duration"], resolution=body["resolution"],
                      ratio=body["ratio"], created_at=1700000000)
        url = f"http://127.0.0.1:{self.server.server_port}/video.mp4"
        if self.path.startswith("/ark/api/v3/contents/generations/tasks/"):
            native["content"] = {"video_url": url}
            self.respond(native)
        elif self.path.startswith("/minimax/v2/query/video_generation/"):
            native["content"] = {"url": url}
            self.respond({"task": native})
        else:
            self.respond({"error": "unexpected status path"}, 404)


def request(base, path, payload=None):
    headers = {"Authorization": f"Bearer {KEY}"}
    data = None
    if payload is not None:
        data = json.dumps(payload).encode()
        headers["Content-Type"] = "application/json"
    with urlopen(Request(base + path, data=data, headers=headers), timeout=30) as response:
        result = response.read()
        if "application/json" in response.headers.get("Content-Type", ""):
            return json.loads(result)
        return result


def main():
    # Import checks above fail fast on old images without the two adapters.
    assert MinimaxVideoConfig and VolcEngineVideoConfig
    server = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        proxy_port = sock.getsockname()[1]
    provider_base = f"http://127.0.0.1:{server.server_port}"
    proxy_base = f"http://127.0.0.1:{proxy_port}"
    models = []
    for model in (SEEDANCE, H3, H3_MAX):
        models.append({"model_name": model, "litellm_params": {
            "model": model, "api_key": "vendor-stub-key",
            "api_base": provider_base + ("/ark" if model == SEEDANCE else "/minimax")}})
    config = {
        "model_list": models,
        "general_settings": {"master_key": KEY},
        "litellm_settings": {"drop_params": False, "num_retries": 0,
                             "user_url_allowed_hosts": [f"127.0.0.1:{server.server_port}"]},
        "router_settings": {"num_retries": 0},
    }
    env = dict(os.environ, LITELLM_MASTER_KEY=KEY, LITELLM_LOCAL_MODEL_COST_MAP="True",
               LITELLM_TELEMETRY="False", NO_PROXY="127.0.0.1,localhost")
    # Do not accidentally use a host's database/logging configuration.
    for name in ("DATABASE_URL", "DIRECT_URL", "REDIS_URL"):
        env.pop(name, None)
    try:
        with tempfile.TemporaryDirectory(prefix="media-contract-") as directory:
            path = Path(directory) / "config.yaml"
            path.write_text(yaml.safe_dump(config))
            with (Path(directory) / "proxy.log").open("w+") as log:
                process = subprocess.Popen(
                    ["litellm", "--config", str(path), "--host", "127.0.0.1", "--port", str(proxy_port)],
                    stdout=log, stderr=subprocess.STDOUT, env=env,
                )
                try:
                    deadline = time.monotonic() + 90
                    while True:
                        try:
                            request(proxy_base, "/v1/models")
                            break
                        except (URLError, TimeoutError, ConnectionError):
                            if process.poll() is not None or time.monotonic() >= deadline:
                                raise RuntimeError("LiteLLM proxy failed to start")
                            time.sleep(.25)
                    cases = [
                        (SEEDANCE, "720p", "960x960", "1:1"),
                        (H3, "768P", "1280x720", "16:9"),
                        (H3, "2K", "2560x1440", "16:9"),
                        (H3_MAX, "480P", "1280x720", "16:9"),
                        (H3_MAX, "768P", "1280x720", "16:9"),
                    ]
                    for model, resolution, size, ratio in cases:
                        Provider.status = "succeeded"
                        created = request(proxy_base, "/v1/videos", {
                            "model": model, "prompt": "Ocean waves", "seconds": "5",
                            "size": size, "resolution": resolution})
                        assert created["id"] and created["status"] == "queued", created
                        native_path, native_body = Provider.creates[-1]
                        assert native_body["resolution"] == resolution, native_body
                        assert native_body["ratio"] == ratio, native_body
                        task_path = "/v1/videos/" + quote(created["id"], safe="")
                        polled = request(proxy_base, task_path)
                        assert polled["status"] == "completed", polled
                        assert request(proxy_base, task_path + "/content") == VIDEO_BYTES
                        for native_status, unified_status in [("running", "in_progress"), ("failed", "failed"), ("cancelled", "cancelled")]:
                            Provider.status = native_status
                            assert request(proxy_base, task_path)["status"] == unified_status
                        print(f"PASS {model} {resolution}: create / status / content / failure / cancel", flush=True)
                    assert len(Provider.creates) == len(cases), "duplicate upstream submissions"
                    print(f"PASS LiteLLM {importlib.metadata.version('litellm')} real proxy contract; no paid calls", flush=True)
                except Exception:
                    log.flush()
                    log.seek(0)
                    print(log.read()[-16000:])
                    raise
                finally:
                    process.terminate()
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
    finally:
        server.shutdown()
        server.server_close()


if __name__ == "__main__":
    main()
