#!/usr/bin/env bash
# Runs the --mode baseline and --mode scripted eval passes with a GENUINELY
# agent-disabled worker (worker-baseline, no provider key configured at all -
# see infra/docker-compose.yml), then restores the normal agent-enabled
# `worker` service. Never run this while an --mode agent pass (or anything
# else depending on the normal `worker` service) is in flight - it stops
# that service.
set -euo pipefail
cd "$(dirname "$0")/../.."

COMPOSE="docker compose -f infra/docker-compose.yml"
# Prefer python3 - many fresh Linux/Mac installs have no bare `python` on
# PATH at all, only python3.
PYTHON="$(command -v python3 || command -v python)"

echo "Stopping the agent-enabled worker..."
$COMPOSE stop worker

echo "Starting the agent-disabled worker-baseline..."
$COMPOSE --profile baseline up -d --build worker-baseline

echo "Running --mode baseline..."
"$PYTHON" evals/scripts/run_eval.py --mode baseline

echo "Running --mode scripted..."
"$PYTHON" evals/scripts/run_eval.py --mode scripted

echo "Stopping worker-baseline..."
$COMPOSE stop worker-baseline

echo "Restarting the agent-enabled worker..."
$COMPOSE up -d worker

echo "Done. See evals/results/eval_results_baseline.json and eval_results_scripted.json"
