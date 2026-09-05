#!/bin/bash
set -euo pipefail
cd /app/gates
git apply /solution/fix.patch
