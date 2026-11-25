#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
VENV_BIN="${REPO_ROOT}/.venv/bin"

TEX_FILE="${SCRIPT_DIR}/microresonance_preprint_draft.tex"
PDF_FILE="${SCRIPT_DIR}/microresonance_preprint_draft.pdf"

# 1. Regenerate figures if requested (--regen-figs) or if missing
if [[ "${1:-}" == "--regen-figs" ]] || [[ ! -f "${SCRIPT_DIR}/figures/fig1_islands_state_space.png" ]]; then
    echo "[1/3] Generating 300-DPI paper figures from raw JSONL..."
    if [[ -x "${VENV_BIN}/python" ]]; then
        "${VENV_BIN}/python" "${REPO_ROOT}/scripts/generate_paper_figures.py"
    else
        python3 "${REPO_ROOT}/scripts/generate_paper_figures.py"
    fi
else
    echo "[1/3] Figures already present in paper/figures/ (pass --regen-figs to rebuild)."
fi

# 2. Locate or install tectonic (single-binary LaTeX engine)
TECTONIC_CMD=""
if command -v tectonic >/dev/null 2>&1; then
    TECTONIC_CMD="$(command -v tectonic)"
elif [[ -x "${VENV_BIN}/tectonic" ]]; then
    TECTONIC_CMD="${VENV_BIN}/tectonic"
else
    echo "[2/3] Installing tectonic LaTeX engine into ${VENV_BIN}..."
    mkdir -p "${VENV_BIN}"
    TAR_URL="$(curl -fsSL https://api.github.com/repos/tectonic-typesetting/tectonic/releases/latest \
        | grep -oE 'https://[^"]+x86_64-unknown-linux-gnu\.tar\.gz' | head -n 1)"
    curl -fsSL "${TAR_URL}" | tar -xz -C "${VENV_BIN}"
    TECTONIC_CMD="${VENV_BIN}/tectonic"
fi
echo "[2/3] Using LaTeX engine: ${TECTONIC_CMD} ($("${TECTONIC_CMD}" --version))"

# 3. Compile LaTeX -> PDF
echo "[3/3] Compiling $(basename "${TEX_FILE}") -> $(basename "${PDF_FILE}")..."
(
    cd "${SCRIPT_DIR}"
    "${TECTONIC_CMD}" -X compile "$(basename "${TEX_FILE}")"
)

if [[ -f "${PDF_FILE}" ]]; then
    echo "SUCCESS: Built ${PDF_FILE} ($(du -h "${PDF_FILE}" | cut -f1))"
else
    echo "ERROR: PDF was not produced." >&2
    exit 1
fi
