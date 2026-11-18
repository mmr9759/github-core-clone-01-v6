#!/usr/bin/env python3
"""Entrypoint for the GitForge v6 entry.

The forge itself is the vendored statically linked Go binary `forge` next to
this file; this entrypoint only execs it so the trial runner's
`python3 /bot/main.py` starts the server as one process.
"""

import os
import sys


def main() -> None:
    here = os.path.dirname(os.path.abspath(__file__))
    forge = os.path.join(here, "forge")
    os.execv(forge, [forge] + sys.argv[1:])


if __name__ == "__main__":
    main()
