"""A single-model, bounded CPU provider; it starts only by explicit invocation."""
import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import signal

from .contracts import ContractError, MODEL_ID, PROFILES, REVISION
from .model import BusyError, Provider

MAX_REQUEST_BYTES = 65536


def decode_request(raw):
    def unique(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ContractError("duplicate JSON key")
            result[key] = value
        return result
    def nonfinite(value):
        raise ContractError("nonfinite JSON value")
    try:
        return json.loads(raw, object_pairs_hook=unique, parse_constant=nonfinite)
    except (ValueError, UnicodeError) as error:
        raise ContractError("invalid request JSON: " + str(error)) from error


def make_server(provider, host="127.0.0.1", port=0):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def reply(self, status, value):
            raw = json.dumps(value, ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            if status == 429:
                self.send_header("Retry-After", "1")
            self.end_headers()
            try:
                self.wfile.write(raw)
            except (BrokenPipeError, ConnectionResetError):
                pass  # A disconnected client receives no result; no background retry.

        def do_GET(self):
            if self.path == "/v1/models":
                self.reply(200, {"object": "list", "data": [{"id": MODEL_ID, "object": "model", "created": 0, "owned_by": "BAAI"}]})
            elif self.path == "/health":
                self.reply(200, {"status": "ready", "model": MODEL_ID, "revision": REVISION})
            else:
                self.reply(404, {"error": {"type": "not_found"}})

        def do_POST(self):
            if self.path != "/v1/representations":
                self.reply(404, {"error": {"type": "not_found"}})
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if not 0 < length <= MAX_REQUEST_BYTES or self.headers.get("Transfer-Encoding"):
                    raise ContractError("bounded Content-Length required")
                if self.headers.get_content_type() != "application/json":
                    raise ContractError("application/json required")
                self.connection.settimeout(10)
                raw = self.rfile.read(length)
                if len(raw) != length:
                    raise ContractError("incomplete request body")
                request = decode_request(raw)
                result = provider.represent(request)
            except BusyError as error:
                self.reply(429, {"error": {"type": "provider_busy", "message": str(error)}})
                return
            except (ContractError, TimeoutError, ValueError) as error:
                self.reply(422, {"error": {"type": "invalid_representation_request", "message": str(error)}})
                return
            except Exception:
                self.reply(500, {"error": {"type": "inference_failed"}})
                return
            self.reply(200, result)
    return ThreadingHTTPServer((host, port), Handler)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-directory", type=Path, required=True)
    parser.add_argument("--model-lock", type=Path, required=True)
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--profiles", action="store_true", help="print fixed profiles and exit")
    args = parser.parse_args()
    if args.profiles:
        print(json.dumps(PROFILES, indent=2))
        return
    provider = Provider(args.model_directory, args.model_lock)
    server = make_server(provider, port=args.port)
    def stop(*_):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, stop)
    print(json.dumps({"event": "ready", "endpoint": f"http://127.0.0.1:{server.server_port}", "model": MODEL_ID}), flush=True)
    try:
        server.serve_forever(poll_interval=0.2)
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()

if __name__ == "__main__":
    main()
