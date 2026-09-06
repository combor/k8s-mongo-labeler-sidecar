#!/usr/bin/env bash
set +x
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
exec python3 "${ROOT_DIR}/test/integration/run.py"
