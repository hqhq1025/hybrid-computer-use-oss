#!/usr/bin/env bash
set -euo pipefail

source /etc/profile.d/osw-workstation-deps.sh

python_path="$(command -v python3)"
python_real_path="$(readlink -f "${python_path}")"
printf 'python_path=%s python_real_path=%s\n' \
    "${python_path}" "${python_real_path}"
if [[ "${python_real_path}" != /usr/bin/python3.10 ]]; then
    printf 'unexpected default Python interpreter: %s\n' \
        "${python_real_path}" >&2
    exit 1
fi

for command in \
    rg sqlite3 identify tesseract ffmpeg ffprobe pdfinfo pdftotext qpdf \
    sox node npm npx playwright growpart parted gio file strings bsdtar \
    unzip zipinfo wmctrl xdotool xclip xsel soffice unoconv
do
    if ! command_path="$(command -v "${command}")"; then
        printf 'missing command: %s\n' "${command}" >&2
        exit 1
    fi
    printf 'command_ok=%s path=%s\n' "${command}" "${command_path}"
done

python3 - <<'PY'
import importlib

modules = [
    "Crypto", "PIL", "PyPDF2", "bs4", "cairosvg", "chardet", "cv2", "docx",
    "filetype", "fitz", "imageio", "imageio_ffmpeg", "lxml", "matplotlib",
    "mutagen", "numpy", "odf", "openpyxl", "pandas", "pdf2image", "pdfminer",
    "pdfplumber", "pdfrw", "playwright", "plotly", "pptx", "pychrome",
    "pydub", "pyexcel_ods3", "pypdf", "pysrt", "pytesseract", "rapidfuzz",
    "reportlab", "requests", "scipy", "seaborn", "selenium", "soundfile",
    "websocket", "websockets", "xlrd", "xlsxwriter", "yaml",
]
for name in modules:
    try:
        importlib.import_module(name)
    except Exception as exc:
        raise RuntimeError(f"module import failed: {name}: {exc}") from exc
print(f"python_modules_ok={len(modules)}")
PY

python3 - <<'PY'
from openpyxl import Workbook, load_workbook
from pathlib import Path

path = Path("/tmp/osw-openpyxl-smoke.xlsx")
workbook = Workbook()
workbook.active["A1"] = "ready"
workbook.save(path)
assert load_workbook(path).active["A1"].value == "ready"
print("openpyxl_roundtrip_ok=true")
PY

python3 - <<'PY'
from pptx import Presentation
from docx import Document

assert Presentation().slides is not None
assert Document().paragraphs == []
print("office_python_smoke_ok=true")
PY

python3 - <<'PY'
import uno
print("uno_import_ok=true")
PY

node - <<'JS'
for (const name of ["playwright", "ws", "chrome-remote-interface"]) {
  require.resolve(name);
}
console.log("node_modules_ok=3");
JS

test -f /usr/lib/libreoffice/program/pythonscript.py
echo "libreoffice_python_provider_ok=true"
