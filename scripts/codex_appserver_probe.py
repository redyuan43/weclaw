#!/usr/bin/env python3
import argparse
import json
import subprocess
import sys
import threading
import time
from queue import Queue, Empty


class AppServerProbe:
    def __init__(self, command, cwd):
        self.proc = subprocess.Popen(
            command,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            cwd=cwd,
            bufsize=1,
        )
        self._next_id = 1
        self._responses = {}
        self._response_events = {}
        self._turn_queues = {}
        self._lock = threading.Lock()
        self._stderr_lines = Queue()

        self._stdout_thread = threading.Thread(target=self._read_stdout, daemon=True)
        self._stderr_thread = threading.Thread(target=self._read_stderr, daemon=True)
        self._stdout_thread.start()
        self._stderr_thread.start()

    def close(self):
        if self.proc.poll() is None:
            self.proc.kill()
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass

    def _read_stderr(self):
        assert self.proc.stderr is not None
        for line in self.proc.stderr:
            self._stderr_lines.put(line.rstrip("\n"))

    def _read_stdout(self):
        assert self.proc.stdout is not None
        for raw in self.proc.stdout:
            raw = raw.strip()
            if not raw:
                continue
            try:
                msg = json.loads(raw)
            except json.JSONDecodeError:
                continue

            if "id" in msg:
                with self._lock:
                    self._responses[msg["id"]] = msg
                    event = self._response_events.get(msg["id"])
                if event is not None:
                    event.set()
                continue

            method = msg.get("method", "")
            params = msg.get("params") or {}
            if method == "item/agentMessage/delta":
                thread_id = params.get("threadId")
                delta = params.get("delta", "")
                if thread_id and delta:
                    self._enqueue_turn_event(thread_id, ("delta", delta))
            elif method == "item/started":
                thread_id = params.get("threadId")
                item = params.get("item") or {}
                if thread_id and item.get("type") == "agentMessage":
                    for content in item.get("content") or []:
                        if content.get("type") == "text" and content.get("text"):
                            self._enqueue_turn_event(thread_id, ("text", content["text"]))
            elif method == "turn/completed":
                thread_id = params.get("threadId")
                if thread_id:
                    self._enqueue_turn_event(thread_id, ("completed", ""))
            elif method == "error":
                for queue in list(self._turn_queues.values()):
                    queue.put(("error", json.dumps(params, ensure_ascii=False)))

    def _enqueue_turn_event(self, thread_id, event):
        with self._lock:
            queue = self._turn_queues.get(thread_id)
        if queue is not None:
            queue.put(event)

    def rpc(self, method, params=None, timeout=30):
        req_id = self._next_id
        self._next_id += 1

        event = threading.Event()
        with self._lock:
            self._response_events[req_id] = event

        payload = {
            "jsonrpc": "2.0",
            "id": req_id,
            "method": method,
        }
        if params is not None:
            payload["params"] = params

        assert self.proc.stdin is not None
        self.proc.stdin.write(json.dumps(payload, ensure_ascii=False) + "\n")
        self.proc.stdin.flush()

        if not event.wait(timeout):
            raise TimeoutError(f"timeout waiting for response to {method}")

        with self._lock:
            response = self._responses.pop(req_id, None)
            self._response_events.pop(req_id, None)

        if response is None:
            raise RuntimeError(f"missing response for {method}")
        if "error" in response:
            raise RuntimeError(json.dumps(response["error"], ensure_ascii=False))
        return response.get("result")

    def notify(self, method, params=None):
        payload = {
            "jsonrpc": "2.0",
            "method": method,
        }
        if params is not None:
            payload["params"] = params
        assert self.proc.stdin is not None
        self.proc.stdin.write(json.dumps(payload, ensure_ascii=False) + "\n")
        self.proc.stdin.flush()

    def initialize(self):
        self.rpc(
            "initialize",
            {
                "clientInfo": {
                    "name": "weclaw-probe",
                    "version": "0.1.0",
                }
            },
        )
        self.notify("initialized")

    def start_thread(self, cwd, model):
        params = {
            "approvalPolicy": "never",
            "cwd": cwd,
            "sandbox": "danger-full-access",
        }
        if model:
            params["model"] = model
        result = self.rpc("thread/start", params)
        thread = (result or {}).get("thread") or {}
        thread_id = thread.get("id")
        if not thread_id:
            raise RuntimeError("thread/start returned empty thread id")
        with self._lock:
            self._turn_queues[thread_id] = Queue()
        return thread_id

    def run_prompt(self, thread_id, prompt, cwd, model, timeout=120):
        self.rpc(
            "turn/start",
            {
                "threadId": thread_id,
                "approvalPolicy": "never",
                "input": [{"type": "text", "text": prompt}],
                "sandboxPolicy": {"type": "dangerFullAccess"},
                "cwd": cwd,
                "model": model,
            },
            timeout=timeout,
        )

        with self._lock:
            queue = self._turn_queues[thread_id]

        parts = []
        deadline = time.time() + timeout
        while time.time() < deadline:
            remaining = max(0.1, deadline - time.time())
            try:
                kind, payload = queue.get(timeout=remaining)
            except Empty:
                continue
            if kind == "error":
                raise RuntimeError(payload)
            if kind in ("delta", "text"):
                parts.append(payload)
            if kind == "completed":
                return "".join(parts).strip()

        raise TimeoutError("timeout waiting for turn completion")

    def stderr_dump(self):
        lines = []
        while True:
            try:
                lines.append(self._stderr_lines.get_nowait())
            except Empty:
                break
        return lines


def main():
    parser = argparse.ArgumentParser(description="Probe codex app-server over stdio.")
    parser.add_argument("--binary", default="/home/nx/.nvm/versions/node/v22.20.0/bin/codex")
    parser.add_argument("--cwd", default="/home/nx/github/weclaw")
    parser.add_argument("--model", default="gpt-5.1-codex-mini")
    parser.add_argument("--config", action="append", default=[], help="Repeatable codex -c key=value")
    parser.add_argument("--prompt", action="append", required=True, help="Prompt to run in a fresh thread")
    args = parser.parse_args()

    command = [args.binary, "app-server", "--listen", "stdio://"]
    for config in args.config:
        command.extend(["-c", config])

    probe = AppServerProbe(command, args.cwd)
    try:
        probe.initialize()
        for prompt in args.prompt:
            thread_id = probe.start_thread(args.cwd, args.model)
            reply = probe.run_prompt(thread_id, prompt, args.cwd, args.model)
            print("=" * 80)
            print(f"PROMPT: {prompt}")
            print(f"THREAD: {thread_id}")
            print("REPLY:")
            print(reply)
        stderr_lines = probe.stderr_dump()
        if stderr_lines:
            print("=" * 80)
            print("STDERR:")
            for line in stderr_lines:
                print(line)
    finally:
        probe.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(1)
