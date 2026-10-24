"""Exec liveness probe: fail if the consumer loop has not written a heartbeat recently."""

import os
import sys
import time

MAX_AGE_SECONDS = int(os.getenv("HEARTBEAT_MAX_AGE_SECONDS", "30"))

try:
    with open(os.getenv("HEARTBEAT_FILE", "/tmp/heartbeat")) as f:
        age = time.time() - float(f.read().strip())
except (OSError, ValueError):
    sys.exit(1)

sys.exit(0 if age < MAX_AGE_SECONDS else 1)
