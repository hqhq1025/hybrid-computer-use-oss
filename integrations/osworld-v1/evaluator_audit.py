#!/usr/bin/env python3
"""Independent evaluator audit overlay for OSWorld runs.

The official score is immutable and remains the comparison score.  This module
adds a second, evidence-backed adjudication layer for known evaluator, setup,
and instruction-contract defects.
"""

from __future__ import annotations

import argparse
import collections
import hashlib
import json
import math
import re
import subprocess
from datetime import date, datetime, timezone
from pathlib import Path

from evaluation_policy import GOOGLE_DRIVE_TASK_IDS

ROOT = Path(__file__).resolve().parent
AUDIT_VERSION = 1

AGE_TASK = "4e6fcf72-daf3-439f-a232-c434ce416af6"
PDF_TASK = "4bcb1253-a636-4df4-8cb0-a35c04dfef31"
VLC_TASK = "fba2c100-79e8-42df-ae74-b592418d54f4"
SSH_TASK = "5812b315-e7bd-4265-b51f-863c02174c28"
MOVIE_TASK = "a9f325aa-8c05-4e4f-8341-9e4358565f4f"
TERMINAL_TASK = "4127319a-8b79-4410-b58a-7a151e15f3d7"
CONDA_TASK = "48d05431-6cd5-4e76-82eb-12b60d823f7d"
FORCE_QUIT_TASK = "2b9493d7-49b8-493a-a71b-56cd1f4d6908"
CALC_TOTAL_GROWTH_CHART_TASK = "0326d92d-d218-48a8-9ca1-981cd6d064c7"
CALC_TOTAL_LINE_CHART_TASK = "0a2e43bf-b26c-4631-a966-af9dfa12c9e5"
CALC_SALES_COGS_CHART_TASK = "12382c62-0cd1-4bf2-bdc8-1d20bf9b2371"
CALC_TWO_COST_CHARTS_TASK = "347ef137-7eeb-4c80-a3bb-0951f26a8aff"
CALC_PERIOD_RATE_TASK = "21ab7b40-77c2-4ae6-8321-e00d3a086c73"
CALC_ZOOM_TASK = "1334ca3e-f9e3-4db8-9ca7-b4c653be7d17"
CALC_SPARKLINE_TASK = "2bd59342-0664-4ccb-ba87-79379096cc08"
IMPRESS_EXACT_GREEN_TASK = "04578141-1d42-4146-b9cf-6fab4ce5fd74"
IMPRESS_NOTES_TASK = "7dbc52a6-11e0-4c9a-a2cb-1e36cfda80d8"
MEDIA_MP3_TASK = "8f080098-ddb1-424c-b438-4e96e5e4786e"

CALC_CHART_SERIALIZATION_TASKS = {
    CALC_TOTAL_GROWTH_CHART_TASK: {
        "result_name": "SalesRep.xlsx",
        "sheet_name": "Sheet1",
        "chart_props": ("type", "title"),
        "compare_sheet": "data_only",
    },
    CALC_TOTAL_LINE_CHART_TASK: {
        "result_name": "SalesRep.xlsx",
        "sheet_name": "Sheet1",
        "chart_props": ("type",),
        "compare_sheet": "data_only",
    },
    CALC_SALES_COGS_CHART_TASK: {
        "result_name": "WeeklySales.xlsx",
        "sheet_name": "Sheet2",
        "chart_props": ("type", "title"),
        "compare_sheet": None,
    },
    CALC_TWO_COST_CHARTS_TASK: {
        "result_name": "Create_column_charts_using_statistics.xlsx",
        "sheet_name": "Sheet1",
        "chart_props": ("type", "direction"),
        "compare_sheet": "formulas",
    },
}

KNOWN_TASKS = {
    AGE_TASK,
    PDF_TASK,
    VLC_TASK,
    SSH_TASK,
    MOVIE_TASK,
    TERMINAL_TASK,
    CONDA_TASK,
    FORCE_QUIT_TASK,
    *CALC_CHART_SERIALIZATION_TASKS,
    CALC_PERIOD_RATE_TASK,
    CALC_ZOOM_TASK,
    CALC_SPARKLINE_TASK,
    IMPRESS_EXACT_GREEN_TASK,
    IMPRESS_NOTES_TASK,
    MEDIA_MP3_TASK,
}

AUDIT_CLASSIFICATIONS = {
    "EVAL_BUG",
    "SETUP_BUG",
    "AMBIGUOUS",
    "VALID_PASS",
    "VALID_FAIL",
    "NEEDS_REVIEW",
}
BASH_EQUIVALENCE_STATUSES = {
    "allowed",
    "disallowed_by_instruction",
    "visible_state_required",
    "needs_review",
}
MCP_MUTATION_TOOLS = {
    "click",
    "double_click",
    "right_click",
    "drag",
    "drag_xy",
    "scroll",
    "type_text",
    "press_key",
    "set_value",
    "invoke_element_action",
}
GUI_NON_MUTATION_ACTIONS = {"screenshot", "wait"}
SUSPICIOUS_PASS_AGENT_SECONDS = 600
SUSPICIOUS_PASS_INPUT_TOKENS = 4_000_000


def _audit_record(
    task_id,
    official_score,
    classification,
    reason,
    *,
    score_lower=None,
    score_upper=None,
    include=True,
    evidence=None,
    bash_status="needs_review",
    bash_reason="",
):
    exact_score = score_lower if score_lower == score_upper else None
    return {
        "version": AUDIT_VERSION,
        "task_id": task_id,
        "classification": classification,
        "official_score": official_score,
        "audited_score": exact_score,
        "score_lower": score_lower,
        "score_upper": score_upper,
        "include_in_capability_denominator": include,
        "reason": reason,
        "evidence": evidence or {},
        "bash_equivalence": {
            "status": bash_status,
            "reason": bash_reason,
        },
    }


def _find_file(directory, exact_name=None, pattern=None):
    directory = Path(directory)
    if exact_name:
        path = directory / exact_name
        if path.exists():
            return path
    if pattern:
        matches = sorted(directory.glob(pattern))
        if matches:
            return matches[0]
    return None


def _cache_file(task_id, exact_name=None, pattern=None):
    return _find_file(ROOT / "cache" / task_id, exact_name, pattern)


def _artifact_file(trace_dir, exact_name=None, pattern=None):
    return _find_file(Path(trace_dir) / "artifacts", exact_name, pattern)


def _evidence_file(task_id, exact_name):
    return _find_file(
        ROOT / "analysis/evaluator_audit_evidence" / task_id,
        exact_name,
    )


def _full_years(birthday, as_of):
    if isinstance(birthday, datetime):
        birthday = birthday.date()
    return (
        as_of.year
        - birthday.year
        - ((as_of.month, as_of.day) < (birthday.month, birthday.day))
    )


def _canonical_sheet_reference(formula):
    if not isinstance(formula, str):
        return ""

    def unquote(match):
        return match.group(1).replace("''", "'") + "!"

    return re.sub(r"'((?:[^']|'')+)'!", unquote, formula)


def _chart_title(chart):
    try:
        return chart.title.tx.rich.p[0].r[0].t
    except (AttributeError, IndexError, TypeError):
        return None


def _chart_signatures(workbook, sheet_name, chart_props):
    if sheet_name not in workbook.sheetnames:
        return []
    signatures = []
    for chart in workbook[sheet_name]._charts:
        series = []
        for item in chart.series:
            value_ref = ""
            category_ref = ""
            if getattr(getattr(item, "val", None), "numRef", None):
                value_ref = item.val.numRef.f or ""
            elif getattr(getattr(item, "val", None), "strRef", None):
                value_ref = item.val.strRef.f or ""
            if getattr(getattr(item, "cat", None), "numRef", None):
                category_ref = item.cat.numRef.f or ""
            elif getattr(getattr(item, "cat", None), "strRef", None):
                category_ref = item.cat.strRef.f or ""
            series.append(
                (
                    _canonical_sheet_reference(value_ref),
                    _canonical_sheet_reference(category_ref),
                )
            )
        props = {}
        if "type" in chart_props:
            props["type"] = chart.tagname
        if "title" in chart_props:
            props["title"] = _chart_title(chart)
        signatures.append({"series": series, "props": props})
    return signatures


def _sheet_values(workbook, sheet_name):
    if sheet_name not in workbook.sheetnames:
        return None
    sheet = workbook[sheet_name]
    return [
        [cell.value for cell in row]
        for row in sheet.iter_rows(
            min_row=1,
            max_row=sheet.max_row,
            min_col=1,
            max_col=sheet.max_column,
        )
    ]


def _sheet_values_equal(left, right):
    if left is None or right is None or len(left) != len(right):
        return False
    return all(
        len(left_row) == len(right_row)
        and all(
            _numbers_equal(left_value, right_value)
            for left_value, right_value in zip(left_row, right_row)
        )
        for left_row, right_row in zip(left, right)
    )


def audit_calc_chart_serialization(task_id, trace_dir, official_score):
    from openpyxl import load_workbook

    spec = CALC_CHART_SERIALIZATION_TASKS[task_id]
    result_path = _artifact_file(trace_dir, spec["result_name"])
    gold_path = _evidence_file(task_id, "gold.xlsx")
    if not result_path or not gold_path:
        return _audit_record(
            task_id,
            official_score,
            "NEEDS_REVIEW",
            "The chart serialization audit is missing the result or preserved gold workbook.",
            score_lower=0.0,
            score_upper=1.0,
            evidence={
                "result_present": bool(result_path),
                "gold_present": bool(gold_path),
            },
            bash_status="allowed",
            bash_reason="The instruction is outcome-based; workbook creation through Bash is valid.",
        )

    result = load_workbook(result_path, data_only=False)
    gold = load_workbook(gold_path, data_only=False)
    result_charts = _chart_signatures(
        result, spec["sheet_name"], spec["chart_props"]
    )
    gold_charts = _chart_signatures(
        gold, spec["sheet_name"], spec["chart_props"]
    )

    sheet_data_equal = True
    if spec["compare_sheet"]:
        data_only = spec["compare_sheet"] == "data_only"
        result_data = load_workbook(result_path, data_only=data_only)
        gold_data = load_workbook(gold_path, data_only=data_only)
        sheet_data_equal = _sheet_values_equal(
            _sheet_values(result_data, spec["sheet_name"]),
            _sheet_values(gold_data, spec["sheet_name"]),
        )

    passed = bool(result_charts) and result_charts == gold_charts
    passed = passed and sheet_data_equal
    score = 1.0 if passed else 0.0
    classification = (
        "VALID_PASS"
        if passed and official_score == 1.0
        else "EVAL_BUG"
        if passed
        else "VALID_FAIL"
    )
    return _audit_record(
        task_id,
        official_score,
        classification,
        (
            "The official chart evaluator compares serialized formula strings and rejects the "
            "OOXML-equivalent quoted sheet reference form."
            if passed and official_score != 1.0
            else "The raw chart score agrees with the independently verified native chart and preserved worksheet."
            if passed
            else "The result still differs after canonicalizing equivalent sheet-reference syntax."
        ),
        score_lower=score,
        score_upper=score,
        evidence={
            "canonical_chart_signatures_equal": result_charts == gold_charts,
            "sheet_data_equal": sheet_data_equal,
            "sheet_compare_mode": spec["compare_sheet"],
            "result_chart_signatures": result_charts,
            "gold_chart_signatures": gold_charts,
        },
        bash_status="allowed",
        bash_reason="The instruction is outcome-based; workbook creation through Bash is valid.",
    )


def _cell_is_green(cell):
    color = cell.font.color
    if color is None or color.type != "rgb":
        return False
    return str(color.rgb).upper().endswith("00FF00")


def _numbers_equal(left, right):
    if isinstance(left, (int, float)) and isinstance(right, (int, float)):
        if math.isnan(float(left)) and math.isnan(float(right)):
            return True
        return math.isclose(float(left), float(right), rel_tol=1e-9, abs_tol=1e-9)
    return left == right


def audit_calc_period_rate(trace_dir, official_score):
    from openpyxl import load_workbook

    result_path = _artifact_file(trace_dir, "PeriodRate.xlsx")
    gold_path = _evidence_file(CALC_PERIOD_RATE_TASK, "gold.xlsx")
    if not result_path or not gold_path:
        return _audit_record(
            CALC_PERIOD_RATE_TASK,
            official_score,
            "NEEDS_REVIEW",
            "The period-rate audit is missing the result or preserved gold workbook.",
            score_lower=0.0,
            score_upper=1.0,
            evidence={
                "result_present": bool(result_path),
                "gold_present": bool(gold_path),
            },
            bash_status="allowed",
            bash_reason="The instruction is outcome-based; workbook editing through Bash is valid.",
        )

    result = load_workbook(result_path, data_only=True)["Sheet1"]
    gold = load_workbook(gold_path, data_only=True)["Sheet1"]
    data_equal = (
        result.max_row == gold.max_row
        and result.max_column == gold.max_column
        and all(
            _numbers_equal(
                result.cell(row, column).value,
                gold.cell(row, column).value,
            )
            for row in range(1, gold.max_row + 1)
            for column in range(1, gold.max_column + 1)
        )
    )

    header = result.cell(1, 3).value
    numeric_rates = [
        (row, result.cell(row, 3).value)
        for row in range(2, result.max_row + 1)
        if isinstance(result.cell(row, 3).value, (int, float))
    ]
    max_value = max((value for _, value in numeric_rates), default=None)
    expected_green_rows = {
        row
        for row, value in numeric_rates
        if max_value is not None and _numbers_equal(value, max_value)
    }
    actual_green_rows = {
        row
        for row in range(2, result.max_row + 1)
        if _cell_is_green(result.cell(row, 3))
    }
    target_style_correct = (
        bool(expected_green_rows)
        and actual_green_rows == expected_green_rows
    )
    passed = (
        data_equal
        and header == "Period Rate (%)"
        and target_style_correct
    )
    score = 1.0 if passed else 0.0
    return _audit_record(
        CALC_PERIOD_RATE_TASK,
        official_score,
        "EVAL_BUG" if passed else "VALID_FAIL",
        (
            "The result has the correct numeric period rates and green maximum; the official "
            "style evaluator rejects inherited theme black as unequal to explicit RGB black."
            if passed
            else "The result does not satisfy the requested numeric values and target green highlight."
        ),
        score_lower=score,
        score_upper=score,
        evidence={
            "sheet_data_equal": data_equal,
            "header": header,
            "expected_green_rows": sorted(expected_green_rows),
            "actual_green_rows": sorted(actual_green_rows),
            "target_style_correct": target_style_correct,
        },
        bash_status="allowed",
        bash_reason="The instruction is outcome-based; workbook editing through Bash is valid.",
    )


def _mcp_result_text(item):
    result = item.get("result") or {}
    return "".join(
        part.get("text", "")
        for part in result.get("content", [])
        if isinstance(part, dict) and part.get("type") == "text"
    )


def _mcp_call_mutates(tool, arguments):
    if tool in {
        "click",
        "set_value",
        "type_text",
        "press_key",
        "invoke_element_action",
        "scroll",
    }:
        return True
    if tool != "gui":
        return False
    return any(
        action.get("type") not in {"screenshot", "wait"}
        for action in (arguments or {}).get("actions", [])
        if isinstance(action, dict)
    )


def audit_calc_zoom(trace_dir, official_score):
    trace_path = Path(trace_dir) / "agent.jsonl"
    if not trace_path.exists():
        return _audit_record(
            CALC_ZOOM_TASK,
            official_score,
            "NEEDS_REVIEW",
            "The visible-state zoom audit is missing the agent trajectory.",
            score_lower=0.0,
            score_upper=1.0,
            bash_status="visible_state_required",
            bash_reason="The requested outcome is the live Calc zoom state.",
        )

    last_mutation = -1
    confirmations = []
    for sequence, line in enumerate(trace_path.read_text(errors="replace").splitlines()):
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        item = event.get("item") or {}
        if (
            event.get("type") != "item.completed"
            or item.get("type") != "mcp_tool_call"
        ):
            continue
        tool = item.get("tool")
        arguments = item.get("arguments") or {}
        if _mcp_call_mutates(tool, arguments):
            last_mutation = sequence
        text = _mcp_result_text(item)
        if (
            tool in {"find", "get_app_state"}
            and re.search(r'\b(?:label\s+)?["\']?200%["\']?', text)
        ):
            confirmations.append(
                {"sequence": sequence, "tool": tool, "excerpt": text[:1000]}
            )

    final_confirmations = [
        item for item in confirmations if item["sequence"] > last_mutation
    ]
    passed = bool(final_confirmations)
    score = 1.0 if passed else 0.0
    return _audit_record(
        CALC_ZOOM_TASK,
        official_score,
        "EVAL_BUG" if passed else "VALID_FAIL",
        (
            "The final read-only accessibility observation confirms Calc at 200%; the official "
            "evaluator instead checks whether the live view setting was serialized into XLSX."
            if passed
            else "No final read-only observation confirms that Calc remained zoomed out."
        ),
        score_lower=score,
        score_upper=score,
        evidence={
            "last_mutation_sequence": last_mutation,
            "final_200_percent_confirmations": final_confirmations,
        },
        bash_status="visible_state_required",
        bash_reason="The requested outcome is the live Calc zoom state, which was independently observed.",
    )


def _sparkline_pairs(path):
    import zipfile
    import xml.etree.ElementTree as ET

    x14 = "http://schemas.microsoft.com/office/spreadsheetml/2009/9/main"
    xm = "http://schemas.microsoft.com/office/excel/2006/main"
    with zipfile.ZipFile(path) as archive:
        root = ET.fromstring(archive.read("xl/worksheets/sheet1.xml"))
    pairs = []
    for sparkline in root.iter(f"{{{x14}}}sparkline"):
        formula = sparkline.find(f"{{{xm}}}f")
        target = sparkline.find(f"{{{xm}}}sqref")
        if formula is None or target is None:
            continue
        pairs.append(
            (
                _canonical_sheet_reference(formula.text or "").replace("$", ""),
                (target.text or "").replace("$", ""),
            )
        )
    return sorted(pairs)


def audit_calc_sparklines(trace_dir, official_score):
    import warnings

    from openpyxl import load_workbook

    result_path = _artifact_file(trace_dir, "OrderId_Month_Chart.xlsx")
    initial_path = _evidence_file(CALC_SPARKLINE_TASK, "initial.xlsx")
    if not result_path or not initial_path:
        return _audit_record(
            CALC_SPARKLINE_TASK,
            official_score,
            "NEEDS_REVIEW",
            "The sparkline audit is missing the result or preserved initial workbook.",
            score_lower=0.0,
            score_upper=1.0,
            evidence={
                "result_present": bool(result_path),
                "initial_present": bool(initial_path),
            },
            bash_status="allowed",
            bash_reason="The instruction is outcome-based; workbook editing through Bash is valid.",
        )

    with warnings.catch_warnings():
        warnings.filterwarnings(
            "ignore",
            message="Sparkline Group extension is not supported",
            category=UserWarning,
        )
        result = load_workbook(result_path, data_only=False)["Sheet1"]
    initial = load_workbook(initial_path, data_only=False)["Sheet1"]
    source_data_unchanged = all(
        _numbers_equal(result.cell(row, column).value, initial.cell(row, column).value)
        for row in range(1, 24)
        for column in range(2, 6)
    )
    expected_pairs = sorted(
        (f"Sheet1!C{row}:E{row}", f"F{row}") for row in range(2, 24)
    )
    actual_pairs = _sparkline_pairs(result_path)
    passed = (
        source_data_unchanged
        and result["F1"].value == "Chart"
        and actual_pairs == expected_pairs
    )
    score = 1.0 if passed else 0.0
    return _audit_record(
        CALC_SPARKLINE_TASK,
        official_score,
        "EVAL_BUG" if passed else "VALID_FAIL",
        (
            "The workbook contains one valid Jan-to-Mar sparkline in F2:F23 for every order; "
            "the official task is hard-coded as infeasible."
            if passed
            else "The workbook does not contain the complete requested sparkline mapping."
        ),
        score_lower=score,
        score_upper=score,
        evidence={
            "source_data_unchanged": source_data_unchanged,
            "chart_header": result["F1"].value,
            "sparkline_count": len(actual_pairs),
            "sparkline_pairs_match": actual_pairs == expected_pairs,
            "sparkline_pairs": actual_pairs,
        },
        bash_status="allowed",
        bash_reason="The instruction is outcome-based; workbook editing through Bash is valid.",
    )


def _run_font_color(run):
    if not (run.text or "").strip():
        return None
    color = run.font.color
    if color is None or color.type is None:
        return None
    try:
        rgb = color.rgb
    except AttributeError:
        rgb = None
    if rgb is not None:
        return str(rgb).upper()

    run_properties = getattr(getattr(run, "_r", None), "rPr", None)
    if run_properties is not None:
        for element in run_properties.iter():
            local = element.tag.rsplit("}", 1)[-1]
            if local in {"prstClr", "schemeClr", "sysClr", "scrgbClr"}:
                value = element.get("val") or element.get("lastClr") or ""
                return f"{local.upper()}:{value.upper()}"
    return f"TYPE:{color.type}"


def _slide_text_colors(path):
    from pptx import Presentation

    presentation = Presentation(path)
    shapes = sorted(
        (
            shape
            for shape in presentation.slides[0].shapes
            if getattr(shape, "has_text_frame", False)
            and (shape.text or "").strip()
        ),
        key=lambda shape: shape.top,
    )
    result = []
    for shape in shapes:
        colors = set()
        for paragraph in shape.text_frame.paragraphs:
            for run in paragraph.runs:
                value = _run_font_color(run)
                if value is not None:
                    colors.add(value)
        result.append(
            {
                "shape_id": shape.shape_id,
                "text": (shape.text or "").strip(),
                "colors": sorted(colors),
            }
        )
    return result


def audit_impress_exact_green(trace_dir, official_score):
    result_path = _artifact_file(trace_dir, "45_2.pptx")
    gold_path = _evidence_file(IMPRESS_EXACT_GREEN_TASK, "gold.pptx")
    if not result_path or not gold_path:
        return _audit_record(
            IMPRESS_EXACT_GREEN_TASK,
            official_score,
            "NEEDS_REVIEW",
            "The exact-green audit is missing the result or preserved gold presentation.",
            score_lower=0.0,
            score_upper=1.0,
            evidence={
                "result_present": bool(result_path),
                "gold_present": bool(gold_path),
            },
            bash_status="allowed",
            bash_reason="The instruction is outcome-based; direct presentation editing is valid.",
        )

    result_shapes = _slide_text_colors(result_path)
    gold_shapes = _slide_text_colors(gold_path)
    result_colors = [item["colors"] for item in result_shapes]
    gold_colors = [item["colors"] for item in gold_shapes]
    texts_equal = [item["text"] for item in result_shapes] == [
        item["text"] for item in gold_shapes
    ]
    pure_rgb = [["FFFF00"], ["FF0000"], ["00FF00"]]
    palette_rgb = [["FFFF00"], ["FF0000"], ["00A933"]]

    if texts_equal and result_colors == palette_rgb:
        classification = "EVAL_BUG"
        lower = upper = 1.0
        reason = "The requested top-to-bottom colors match the LibreOffice named palette colors."
    elif texts_equal and result_colors == pure_rgb and gold_colors == palette_rgb:
        classification = "AMBIGUOUS"
        lower, upper = 0.0, 1.0
        reason = (
            "The instruction says exact green without defining a color space. The result uses "
            "pure additive green 00FF00, while the gold uses LibreOffice's named Green 00A933."
        )
    else:
        classification = "VALID_FAIL"
        lower = upper = 0.0
        reason = "The presentation does not match either reasonable exact-green interpretation."

    return _audit_record(
        IMPRESS_EXACT_GREEN_TASK,
        official_score,
        classification,
        reason,
        score_lower=lower,
        score_upper=upper,
        evidence={
            "texts_equal": texts_equal,
            "result_shapes": result_shapes,
            "gold_shapes": gold_shapes,
            "pure_rgb_interpretation": pure_rgb,
            "libreoffice_palette_interpretation": palette_rgb,
        },
        bash_status="allowed",
        bash_reason="The instruction is outcome-based; direct presentation editing is valid.",
    )


def _formula_is_current_age(formula, row):
    if not isinstance(formula, str):
        return False
    normalized = re.sub(r"\s+", "", formula).upper()
    return normalized == f'=DATEDIF(C{row},TODAY(),"Y")'


def audit_age(trace_dir, official_score, as_of):
    from openpyxl import load_workbook

    result_path = _artifact_file(
        trace_dir, "Employee_Age_By_Birthday.xlsx"
    )
    gold_path = _evidence_file(AGE_TASK, "gold.xlsx") or _cache_file(
        AGE_TASK, "Employee_Age_By_Birthday_gold.xlsx"
    )
    if not result_path or not gold_path:
        return _audit_record(
            AGE_TASK,
            official_score,
            "EVAL_BUG",
            "The time-varying age evaluator uses a static cached gold workbook.",
            include=False,
            evidence={"error": "result or gold workbook missing"},
            bash_status="allowed",
            bash_reason="The instruction is outcome-based; any general-purpose channel may produce the correct workbook.",
        )

    result_formula = load_workbook(result_path, data_only=False).active
    result_values = load_workbook(result_path, data_only=True).active
    gold_formula = load_workbook(gold_path, data_only=False).active

    non_target_equal = True
    for row in range(1, max(result_formula.max_row, gold_formula.max_row) + 1):
        for column in range(1, 4):
            if result_formula.cell(row, column).value != gold_formula.cell(
                row, column
            ).value:
                non_target_equal = False

    valid_rows = []
    invalid_rows = []
    for row in range(2, result_formula.max_row + 1):
        birthday = result_values.cell(row, 3).value
        formula = result_formula.cell(row, 4).value
        value = result_values.cell(row, 4).value
        expected = _full_years(birthday, as_of)
        valid = _formula_is_current_age(formula, row) or value == expected
        (valid_rows if valid else invalid_rows).append(
            {
                "row": row,
                "value": value,
                "formula": formula if isinstance(formula, str) else None,
                "expected": expected,
            }
        )

    total = len(valid_rows) + len(invalid_rows)
    score = len(valid_rows) / total if total and non_target_equal else 0.0
    return _audit_record(
        AGE_TASK,
        official_score,
        "EVAL_BUG",
        "Static cached TODAY() values make the official full-sheet comparison date-dependent.",
        score_lower=score,
        score_upper=score,
        evidence={
            "as_of": as_of.isoformat(),
            "non_target_columns_unchanged": non_target_equal,
            "valid_rows": len(valid_rows),
            "total_rows": total,
            "invalid_rows": invalid_rows,
        },
        bash_status="allowed",
        bash_reason="The instruction is outcome-based; any general-purpose channel may produce the correct workbook.",
    )


def _normalized_pdf_pages(path):
    import fitz

    document = fitz.open(path)
    pages = []
    image_counts = []
    dimensions = []
    for page in document:
        pages.append(re.sub(r"\s+", " ", page.get_text()).strip())
        image_counts.append(len(page.get_images(full=True)))
        dimensions.append(
            (round(page.rect.width, 3), round(page.rect.height, 3))
        )
    return document, pages, image_counts, dimensions


def _pdf_visual_scores(result_document, gold_document):
    import fitz
    import numpy as np

    scores = []
    for result_page, gold_page in zip(result_document, gold_document):
        result_pixmap = result_page.get_pixmap(
            colorspace=fitz.csGRAY,
            alpha=False,
        )
        gold_pixmap = gold_page.get_pixmap(
            colorspace=fitz.csGRAY,
            alpha=False,
        )
        if (
            result_pixmap.width != gold_pixmap.width
            or result_pixmap.height != gold_pixmap.height
            or result_pixmap.n != gold_pixmap.n
        ):
            return []
        result_array = np.frombuffer(
            result_pixmap.samples, dtype=np.uint8
        ).reshape(result_pixmap.height, result_pixmap.width, result_pixmap.n)
        gold_array = np.frombuffer(
            gold_pixmap.samples, dtype=np.uint8
        ).reshape(gold_pixmap.height, gold_pixmap.width, gold_pixmap.n)
        difference = np.abs(
            result_array.astype(np.int16)
            - gold_array.astype(np.int16)
        )
        scores.append(
            max(0.0, 1.0 - float(difference.mean()) / 255.0)
        )
    return scores


def audit_pdf(trace_dir, official_score):
    result_path = _artifact_file(
        trace_dir, "View_Person_Organizational_Summary.pdf"
    )
    if not result_path:
        result_path = _evidence_file(PDF_TASK, "result.pdf") or _cache_file(
            PDF_TASK, "Constitution_Template_With_Guidelines_1.pdf"
        )
    gold_path = _evidence_file(PDF_TASK, "gold.pdf") or _cache_file(
        PDF_TASK, "Constitution_Template_With_Guidelines_Gold_1.pdf"
    )
    if not result_path or not gold_path:
        return _audit_record(
            PDF_TASK,
            official_score,
            "EVAL_BUG",
            "The PDF metric returns an unthresholded similarity as task success.",
            include=False,
            evidence={"error": "result or gold PDF missing"},
            bash_status="allowed",
            bash_reason="The instruction requires a correct PDF outcome, not a specific export control surface.",
        )

    result_doc, result_pages, result_images, result_dimensions = (
        _normalized_pdf_pages(result_path)
    )
    gold_doc, gold_pages, gold_images, gold_dimensions = _normalized_pdf_pages(
        gold_path
    )
    try:
        visual_scores = _pdf_visual_scores(result_doc, gold_doc)
    except ImportError as exc:
        result_doc.close()
        gold_doc.close()
        return _audit_record(
            PDF_TASK,
            official_score,
            "NEEDS_REVIEW",
            "The independent PDF visual audit dependency is unavailable.",
            include=False,
            evidence={
                "error": f"{type(exc).__name__}: {exc}",
                "result": str(result_path),
                "gold": str(gold_path),
            },
            bash_status="allowed",
            bash_reason="The task cannot be re-adjudicated without the declared visual dependency.",
        )
    result_doc.close()
    gold_doc.close()
    page_count_equal = len(result_pages) == len(gold_pages)
    text_equal = result_pages == gold_pages
    dimensions_equal = result_dimensions == gold_dimensions
    images_equal = result_images == gold_images
    visual_pass = bool(visual_scores) and min(visual_scores) >= 0.95
    passed = (
        page_count_equal
        and text_equal
        and dimensions_equal
        and images_equal
        and visual_pass
    )
    score = 1.0 if passed else 0.0
    return _audit_record(
        PDF_TASK,
        official_score,
        "EVAL_BUG",
        "Raw extracted-text similarity is not a binary export acceptance rule.",
        score_lower=score,
        score_upper=score,
        evidence={
            "result": str(result_path),
            "gold": str(gold_path),
            "page_count_equal": page_count_equal,
            "normalized_page_text_equal": text_equal,
            "page_dimensions_equal": dimensions_equal,
            "image_counts_equal": images_equal,
            "visual_scores": visual_scores,
            "visual_threshold": 0.95,
        },
        bash_status="allowed",
        bash_reason="The instruction requires a correct PDF outcome, not a specific export control surface.",
    )


def _pre_agent_state(trace_dir):
    path = Path(trace_dir) / "pre_agent_state.json"
    if not path.exists():
        return None
    try:
        return json.loads(path.read_text())
    except json.JSONDecodeError:
        return {"capture_error": "invalid pre_agent_state.json"}


def _initial_vlc_dialog_evidence(trace_dir):
    path = Path(trace_dir) / "agent.jsonl"
    if not path.exists():
        return False
    mutated = False
    for line in path.read_text(errors="replace").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") != "item.completed":
            continue
        item = event.get("item") or {}
        item_type = item.get("type")
        if item_type == "command_execution":
            mutated = True
            continue
        if item_type != "mcp_tool_call":
            continue
        tool = str(item.get("tool") or "")
        normalized_tool = tool.lower()
        arguments = item.get("arguments") or {}
        if normalized_tool in {"ax", "a11y"}:
            mode = str(arguments.get("mode") or "").lower()
            if mode not in {"observe", "find", "assert_ax", "assert_a11y"}:
                mutated = True
                continue
        elif normalized_tool not in {"get_app_state", "find", "gui"}:
            mutated = True
            continue
        if normalized_tool == "gui":
            actions = arguments.get("actions") or []
            if any(action.get("type") != "screenshot" for action in actions):
                mutated = True
        result_text = json.dumps(item.get("result") or {})
        if (
            not mutated
            and "Privacy and Network Access Policy" in result_text
        ):
            return True
    return False


def _vlc_frame_scores(result_path, video_path, start=120.9, end=121.2):
    import cv2
    from skimage.metrics import structural_similarity

    result = cv2.imread(str(result_path), cv2.IMREAD_COLOR)
    if result is None:
        return []
    capture = cv2.VideoCapture(str(video_path))
    fps = capture.get(cv2.CAP_PROP_FPS)
    if not fps or fps <= 0:
        capture.release()
        return []
    first = max(0, int(start * fps))
    last = int(end * fps)
    scores = []
    for frame_index in range(first, last + 1):
        capture.set(cv2.CAP_PROP_POS_FRAMES, frame_index)
        ok, frame = capture.read()
        if not ok:
            continue
        if frame.shape != result.shape:
            capture.release()
            return []
        score = structural_similarity(
            result,
            frame,
            channel_axis=2,
            data_range=255,
        )
        scores.append(
            {
                "frame": frame_index,
                "seconds": frame_index / fps,
                "score": float(score),
            }
        )
    capture.release()
    return scores


def audit_vlc(trace_dir, official_score):
    state = _pre_agent_state(trace_dir)
    if state is not None:
        privacy_dialog = bool(state.get("privacy_dialog_present"))
        setup_source = "independent pre-agent capture"
    else:
        privacy_dialog = _initial_vlc_dialog_evidence(trace_dir)
        setup_source = "first read-only agent observation"

    result_path = _artifact_file(trace_dir, "interstellar.png")
    if not result_path:
        result_path = _evidence_file(VLC_TASK, "result.png")
    video_path = _cache_file(
        VLC_TASK, pattern="*Interstellar Movie - Official Trailer.mp4"
    )
    frame_scores = (
        _vlc_frame_scores(result_path, video_path)
        if result_path and video_path
        else []
    )
    best_frame = (
        max(frame_scores, key=lambda item: item["score"])
        if frame_scores
        else None
    )

    if privacy_dialog:
        return _audit_record(
            VLC_TASK,
            official_score,
            "SETUP_BUG",
            "VLC first-run UI contaminated the task's initial state before the agent acted.",
            include=False,
            evidence={
                "privacy_dialog_observed": True,
                "setup_evidence_source": setup_source,
                "official_similarity": official_score,
                "best_source_frame": best_frame,
                "reference_window_seconds": [120.9, 121.2],
                "secondary_evaluator_issue": (
                    "A single gold frame gives valid adjacent source frames "
                    "less than full credit."
                ),
            },
            bash_status="visible_state_required",
            bash_reason="The requested snapshot must come from the visible current VLC scene.",
        )

    frame_pass = bool(best_frame) and best_frame["score"] >= 0.98
    if frame_pass:
        return _audit_record(
            VLC_TASK,
            official_score,
            "EVAL_BUG",
            "The captured scene matches the allowed source-frame window, but the single-frame gold is over-specific.",
            score_lower=1.0,
            score_upper=1.0,
            evidence={
                "privacy_dialog_observed": False,
                "setup_evidence_source": setup_source,
                "best_source_frame": best_frame,
                "reference_window_seconds": [120.9, 121.2],
                "frame_acceptance_threshold": 0.98,
            },
            bash_status="visible_state_required",
            bash_reason="The requested snapshot must come from the visible current VLC scene.",
        )

    return _audit_record(
        VLC_TASK,
        official_score,
        "VALID_FAIL" if best_frame else "NEEDS_REVIEW",
        (
            "The captured image does not match the allowed source-frame window."
            if best_frame
            else "No independent setup state or decodable result/source frame was available."
        ),
        score_lower=0.0 if best_frame else None,
        score_upper=0.0 if best_frame else None,
        include=bool(best_frame),
        evidence={
            "privacy_dialog_observed": False,
            "setup_evidence_source": setup_source,
            "best_source_frame": best_frame,
            "reference_window_seconds": [120.9, 121.2],
            "frame_acceptance_threshold": 0.98,
        },
        bash_status="visible_state_required",
        bash_reason="The requested snapshot must come from the visible current VLC scene.",
    )


def _command_outputs(trace_dir):
    outputs = []
    path = Path(trace_dir) / "agent.jsonl"
    if not path.exists():
        return outputs
    for line in path.read_text(errors="replace").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        item = event.get("item") or {}
        if (
            event.get("type") == "item.completed"
            and item.get("type") == "command_execution"
        ):
            outputs.append(
                {
                    "command": item.get("command") or "",
                    "output": item.get("aggregated_output") or "",
                    "exit_code": item.get("exit_code"),
                }
            )
    return outputs


def _site_block_evidence(trace_dir):
    path = Path(trace_dir) / "agent.jsonl"
    if not path.exists():
        return None
    recent_mcp_texts = []
    declared_infeasible = False
    for line in path.read_text(errors="replace").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") != "item.completed":
            continue
        item = event.get("item") or {}
        if item.get("type") == "agent_message":
            declared_infeasible = (
                str(item.get("text") or "").strip() == "TASK_INFEASIBLE"
            ) or declared_infeasible
        elif item.get("type") == "mcp_tool_call":
            text = "".join(
                block.get("text", "")
                for block in (item.get("result") or {}).get("content", [])
                if isinstance(block, dict) and block.get("type") == "text"
            )
            if text:
                recent_mcp_texts.append(text)
                recent_mcp_texts = recent_mcp_texts[-5:]

    recent_text = "\n".join(recent_mcp_texts)
    lower = recent_text.lower()
    markers = [
        marker
        for marker in (
            "google.com/sorry",
            "our systems have detected unusual traffic",
            "i'm not a robot",
            "recaptcha",
            "verify you are human",
            "access denied",
            "you don't have permission to access",
            "you do not have permission to access",
        )
        if marker in lower
    ]
    if declared_infeasible and markers:
        return {
            "declared_infeasible": True,
            "markers": markers,
            "recent_observation_excerpt": recent_text[-4000:],
        }
    return None


def _ssh_trace_evidence(trace_dir):
    outputs = _command_outputs(trace_dir)
    records = [
        {
            "command": str(item.get("command") or ""),
            "output": str(item.get("output") or ""),
        }
        for item in outputs
    ]
    effective_config = [
        record
        for record in records
        if (
            "chrootdirectory " in record["output"].lower()
            and "forcecommand " in record["output"].lower()
        )
    ]
    user_config = [
        record
        for record in effective_config
        if "charles:x:" in record["output"]
    ]
    sftp_records = [
        record
        for record in records
        if (
            "sftp" in record["command"].lower()
            or (
                "Connected to 127.0.0.1." in record["output"]
                and "Remote working directory:" in record["output"]
            )
        )
    ]
    forced_sftp = any(
        "forcecommand internal-sftp" in record["output"].lower()
        and "permittty no" in record["output"].lower()
        for record in user_config
    )
    return {
        "user_exists": bool(user_config),
        "chroot_configured": any(
            "chrootdirectory /home/test1" in record["output"].lower()
            for record in user_config
        ),
        "password_sftp_login": any(
            "Connected to 127.0.0.1." in record["output"]
            and "Remote working directory:" in record["output"]
            for record in sftp_records
        ),
        "shell_denied": (
            any(
                "This service allows sftp connections only."
                in record["output"]
                for record in sftp_records
            )
            or (
                forced_sftp
                and any(
                    ":/usr/sbin/nologin" in record["output"]
                    for record in user_config
                )
            )
        ),
        "host_passwd_hidden": any(
            '"/etc/passwd" not found' in record["output"]
            or "Can't ls: \"/etc/passwd\" not found" in record["output"]
            or "Can't ls: \"/etc\" not found" in record["output"]
            for record in sftp_records
        ),
    }


def _ssh_live_evidence(container_name):
    probe = r'''
import json
import os
import pathlib
import pexpect
import pwd
import grp
import subprocess
import uuid

username = "charles"
password = os.environ["OSW_AUDIT_PASSWORD"]
token = ".osw-audit-" + uuid.uuid4().hex
allowed = pathlib.Path("/home/test1")
inside = allowed / token
outside = pathlib.Path("/tmp") / token
escape = allowed / (token + "-escape")
copies = [
    pathlib.Path("/tmp") / (token + "-inside-copy"),
    pathlib.Path("/tmp") / (token + "-outside-copy"),
    pathlib.Path("/tmp") / (token + "-escape-copy"),
]

evidence = {
    "evidence_source": "independent live container probe",
    "user_exists": False,
    "non_privileged_user": False,
    "sshd_config_valid": False,
    "sshd_running": False,
    "password_sftp_login": False,
    "wrong_password_rejected": False,
    "inside_probe_readable": False,
    "outside_probe_blocked": False,
    "symlink_escape_blocked": False,
    "forwarding_disabled": False,
}

def run(command):
    return subprocess.run(
        command,
        text=True,
        capture_output=True,
        timeout=15,
    )

def open_sftp(secret):
    child = pexpect.spawn(
        "sftp",
        [
            "-o", "StrictHostKeyChecking=no",
            "-o", "UserKnownHostsFile=/dev/null",
            "-o", "PreferredAuthentications=password",
            "-o", "PubkeyAuthentication=no",
            f"{username}@127.0.0.1",
        ],
        encoding="utf-8",
        timeout=10,
    )
    index = child.expect([r"[Pp]assword:", r"Permission denied", pexpect.EOF])
    if index != 0:
        return child, False
    child.sendline(secret)
    index = child.expect(
        [r"sftp>", r"Permission denied", r"[Pp]assword:", pexpect.EOF]
    )
    return child, index == 0

def sftp_command(child, command):
    child.sendline(command)
    child.expect("sftp>")
    return child.before

try:
    account = pwd.getpwnam(username)
    evidence["user_exists"] = True
    groups = {
        group.gr_name
        for group in grp.getgrall()
        if username in group.gr_mem or group.gr_gid == account.pw_gid
    }
    evidence["non_privileged_user"] = (
        account.pw_uid != 0
        and not groups.intersection({"sudo", "root", "docker", "lxd"})
    )

    evidence["sshd_config_valid"] = run(
        ["/usr/sbin/sshd", "-t"]
    ).returncode == 0
    evidence["sshd_running"] = run(
        ["pgrep", "-x", "sshd"]
    ).returncode == 0
    effective = run(
        [
            "/usr/sbin/sshd", "-T", "-C",
            f"user={username},host=localhost,addr=127.0.0.1",
        ]
    ).stdout.lower()
    evidence["forwarding_disabled"] = all(
        setting in effective
        for setting in (
            "allowtcpforwarding no",
            "allowagentforwarding no",
            "x11forwarding no",
            "permittunnel no",
        )
    )

    allowed.mkdir(parents=True, exist_ok=True)
    inside.write_text("inside-" + token)
    outside.write_text("outside-" + token)
    inside.chmod(0o444)
    outside.chmod(0o444)
    escape.symlink_to(outside)

    child, logged_in = open_sftp(password)
    evidence["password_sftp_login"] = logged_in
    if logged_in:
        inside_output = sftp_command(
            child, f"get /{token} {copies[0]}"
        )
        if "not found" in inside_output.lower():
            inside_output = sftp_command(
                child, f"get /home/test1/{token} {copies[0]}"
            )
        evidence["inside_probe_readable"] = (
            copies[0].exists()
            and copies[0].read_text() == "inside-" + token
        )

        outside_output = sftp_command(
            child, f"get /tmp/{token} {copies[1]}"
        )
        evidence["outside_probe_blocked"] = (
            not copies[1].exists()
            and any(
                marker in outside_output.lower()
                for marker in ("not found", "permission denied", "failure")
            )
        )

        escape_output = sftp_command(
            child, f"get /{token}-escape {copies[2]}"
        )
        evidence["symlink_escape_blocked"] = (
            not copies[2].exists()
            and any(
                marker in escape_output.lower()
                for marker in ("not found", "permission denied", "failure")
            )
        )
        child.sendline("bye")
        child.expect(pexpect.EOF)

    wrong_child, wrong_login = open_sftp(password + "-wrong")
    evidence["wrong_password_rejected"] = not wrong_login
    wrong_child.close(force=True)
finally:
    for path in [inside, outside, escape, *copies]:
        try:
            path.unlink()
        except FileNotFoundError:
            pass

print(json.dumps(evidence, sort_keys=True))
'''
    completed = subprocess.run(
        [
            "docker",
            "exec",
            "-e",
            "OSW_AUDIT_PASSWORD=Ex@mpleP@55w0rd!",
            container_name,
            "python3",
            "-c",
            probe,
        ],
        text=True,
        capture_output=True,
        timeout=90,
    )
    if completed.returncode != 0:
        return {
            "evidence_source": "independent live container probe",
            "probe_error": (
                completed.stderr.strip()
                or completed.stdout.strip()
                or f"exit {completed.returncode}"
            )[-2000:],
        }
    try:
        return json.loads(completed.stdout.strip().splitlines()[-1])
    except (IndexError, json.JSONDecodeError) as exc:
        return {
            "evidence_source": "independent live container probe",
            "probe_error": f"{type(exc).__name__}: {exc}",
            "probe_stdout": completed.stdout[-2000:],
        }


def audit_ssh(trace_dir, official_score, container_name=None):
    evidence = (
        _ssh_live_evidence(container_name)
        if container_name
        else _ssh_trace_evidence(trace_dir)
    )
    if container_name:
        required = (
            "user_exists",
            "non_privileged_user",
            "sshd_config_valid",
            "sshd_running",
            "password_sftp_login",
            "wrong_password_rejected",
            "inside_probe_readable",
            "outside_probe_blocked",
            "symlink_escape_blocked",
            "forwarding_disabled",
        )
    else:
        required = (
            "user_exists",
            "chroot_configured",
            "password_sftp_login",
            "shell_denied",
        )
    core_pass = all(
        evidence.get(key, False)
        for key in required
    )
    if container_name:
        lower = upper = 1.0 if core_pass else 0.0
    elif core_pass:
        lower, upper = 0.0, 1.0
        evidence["evidence_source"] = "preserved command trajectory"
        evidence["provisional_only"] = True
    else:
        lower = upper = 0.0
        evidence["evidence_source"] = "preserved command trajectory"
    return _audit_record(
        SSH_TASK,
        official_score,
        "EVAL_BUG",
        "The official check does not test SSH confinement and rejects secure SFTP-only chroots.",
        score_lower=lower,
        score_upper=upper,
        evidence=evidence,
        bash_status="allowed",
        bash_reason="This is an operating-system configuration task whose durable state is the contract.",
    )


def _hyphen_case_equivalent(actual, expected):
    def capitalize_after_hyphen(value):
        return re.sub(
            r"(?<=-)([A-Za-z])",
            lambda match: match.group(1).upper(),
            value,
        )

    return capitalize_after_hyphen(actual) == expected


def audit_movie(trace_dir, official_score):
    from openpyxl import load_workbook

    result_path = _artifact_file(
        trace_dir, "Movie_title_garbage_clean.xlsx"
    )
    gold_path = _evidence_file(MOVIE_TASK, "gold.xlsx") or _cache_file(
        MOVIE_TASK, "Movie_title_garbage_clean_gold.xlsx"
    )
    if not result_path or not gold_path:
        return _audit_record(
            MOVIE_TASK,
            official_score,
            "AMBIGUOUS",
            "The binary full-sheet metric hides row-level correctness and a hyphen title-case ambiguity.",
            include=False,
            evidence={"error": "result or gold workbook missing"},
            bash_status="allowed",
            bash_reason="The instruction is outcome-based; any general-purpose channel may produce the correct workbook.",
        )

    result = load_workbook(result_path, data_only=True).active
    gold = load_workbook(gold_path, data_only=True).active
    non_target_equal = True
    for row in range(1, max(result.max_row, gold.max_row) + 1):
        for column in (1, 2):
            if result.cell(row, column).value != gold.cell(row, column).value:
                non_target_equal = False

    exact = []
    ambiguous = []
    wrong = []
    for row in range(2, result.max_row + 1):
        actual = str(result.cell(row, 3).value or "")
        expected = str(gold.cell(row, 3).value or "")
        if actual == expected:
            exact.append(row)
        elif _hyphen_case_equivalent(actual, expected):
            ambiguous.append(
                {"row": row, "actual": actual, "expected": expected}
            )
        else:
            wrong.append({"row": row, "actual": actual, "expected": expected})

    total = len(exact) + len(ambiguous) + len(wrong)
    lower = len(exact) / total if total and non_target_equal else 0.0
    upper = (
        (len(exact) + len(ambiguous)) / total
        if total and non_target_equal
        else 0.0
    )
    return _audit_record(
        MOVIE_TASK,
        official_score,
        "AMBIGUOUS",
        "The instruction does not define whether a hyphen starts another word, and the binary metric loses partial credit.",
        score_lower=lower,
        score_upper=upper,
        evidence={
            "non_target_columns_unchanged": non_target_equal,
            "exact_rows": len(exact),
            "total_rows": total,
            "ambiguous_rows": ambiguous,
            "wrong_rows": wrong,
        },
        bash_status="allowed",
        bash_reason="The instruction is outcome-based; any general-purpose channel may produce the correct workbook.",
    )


def audit_terminal(trace_dir, official_score):
    outputs = _command_outputs(trace_dir)
    shell_has_answer = any(
        re.search(r"(?m)^\s*54(?:\s+total)?\s*$", item["output"])
        for item in outputs
    )
    observations = []
    path = Path(trace_dir) / "agent.jsonl"
    if path.exists():
        for sequence, line in enumerate(
            path.read_text(errors="replace").splitlines()
        ):
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            item = event.get("item") or {}
            if (
                event.get("type") != "item.completed"
                or item.get("type") != "mcp_tool_call"
                or item.get("tool") != "get_app_state"
            ):
                continue
            arguments = item.get("arguments") or {}
            text = _mcp_result_text(item)
            terminal_targeted = (
                str(arguments.get("app") or "").lower() == "terminal"
            )
            focused_terminal = (
                'terminal "Terminal" [focused]' in text
                or "The focused UI element is terminal " in text
            )
            answer_visible = bool(
                re.search(r"(?:\\n|\n)54(?:\\n|\n)", text)
            )
            if terminal_targeted or focused_terminal:
                observations.append(
                    {
                        "sequence": sequence,
                        "tool": item.get("tool"),
                        "terminal_targeted": terminal_targeted,
                        "focused_terminal": focused_terminal,
                        "answer_visible": answer_visible,
                        "excerpt": text[-1200:],
                    }
                )

    visible_answer = any(
        item["focused_terminal"] and item["answer_visible"]
        for item in observations
    )
    if visible_answer or official_score == 1.0:
        return _audit_record(
            TERMINAL_TASK,
            official_score,
            "VALID_PASS" if official_score == 1.0 else "EVAL_BUG",
            (
                "An independent accessibility read confirms that the requested result is visible "
                "in the focused terminal; the official getter only recognizes GNOME Terminal."
                if visible_answer and official_score != 1.0
                else "The official evaluator accepted the visible terminal output."
            ),
            score_lower=1.0,
            score_upper=1.0,
            evidence={
                "background_shell_found_answer": shell_has_answer,
                "visible_terminal_answer": visible_answer,
                "terminal_observations": observations,
            },
            bash_status="visible_state_required",
            bash_reason=(
                "Background shell stdout is insufficient, but the final focused terminal UI "
                "independently exposes the requested result."
            ),
        )

    return _audit_record(
        TERMINAL_TASK,
        official_score,
        "VALID_FAIL",
        "The instruction explicitly requires the result to be visible in the terminal; a background Bash result is not equivalent.",
        score_lower=0.0,
        score_upper=0.0,
        evidence={
            "background_shell_found_answer": shell_has_answer,
            "visible_terminal_answer": False,
            "terminal_observations": observations,
        },
        bash_status="visible_state_required",
        bash_reason="The requested observable state is terminal UI output, not shell stdout captured by the agent.",
    )


def _run_live_probe(container_name, script, timeout=30):
    completed = subprocess.run(
        [
            "docker",
            "exec",
            "-e",
            "HOME=/home/user",
            "-u",
            "user",
            container_name,
            "bash",
            "-lc",
            script,
        ],
        text=True,
        capture_output=True,
        timeout=timeout,
    )
    return {
        "returncode": completed.returncode,
        "stdout": completed.stdout,
        "stderr": completed.stderr,
    }


def audit_conda(trace_dir, official_score, container_name=None):
    if container_name:
        live = _run_live_probe(
            container_name,
            "set -o pipefail; "
            "bash -lc 'command -v conda && conda --version'; "
            "bash -ic 'command -v conda && conda --version' 2>/dev/null",
            timeout=60,
        )
        passed = (
            live["returncode"] == 0
            and live["stdout"].count("conda") >= 2
        )
        lower = upper = 1.0 if passed else 0.0
        evidence = {
            "evidence_source": "independent live container probe",
            **live,
        }
    else:
        combined = "\n".join(
            item["output"] for item in _command_outputs(trace_dir)
        )
        passed = (
            "/home/user/miniconda3/bin/conda" in combined
            and "conda 26.5.3" in combined
        )
        lower, upper = (0.0, 1.0) if passed else (0.0, 0.0)
        evidence = {
            "evidence_source": "preserved command trajectory",
            "fresh_shell_conda_observed": passed,
            "provisional_only": passed,
        }
    return _audit_record(
        CONDA_TASK,
        official_score,
        "EVAL_BUG",
        "The official evaluator checks for one conda-init comment instead of testing whether conda works in fresh shells.",
        score_lower=lower,
        score_upper=upper,
        evidence=evidence,
        bash_status="allowed",
        bash_reason="Installing and configuring a command-line environment is a durable OS state task.",
    )


def audit_force_quit(trace_dir, official_score, container_name=None):
    if container_name:
        live = _run_live_probe(
            container_name,
            "python3 - <<'PY'\n"
            "from pathlib import Path\n"
            "live=[]\n"
            "for proc in Path('/proc').glob('[0-9]*'):\n"
            "    try:\n"
            "        comm=(proc/'comm').read_text().strip()\n"
            "        stat=(proc/'stat').read_text().split()[2]\n"
            "    except (FileNotFoundError, ProcessLookupError, IndexError):\n"
            "        continue\n"
            "    if comm in {'soffice.bin','oosplash'} and stat != 'Z':\n"
            "        live.append({'pid':proc.name,'comm':comm,'stat':stat})\n"
            "print(live)\n"
            "raise SystemExit(bool(live))\n"
            "PY",
        )
        passed = live["returncode"] == 0
        lower = upper = 1.0 if passed else 0.0
        evidence = {
            "evidence_source": "independent live container probe",
            **live,
        }
    else:
        combined = "\n".join(
            item["output"] for item in _command_outputs(trace_dir)
        )
        passed = (
            "[soffice.bin] <defunct>" in combined
            and "[oosplash] <defunct>" in combined
        )
        lower, upper = (0.0, 1.0) if passed else (0.0, 0.0)
        evidence = {
            "evidence_source": "preserved command trajectory",
            "only_zombie_processes_observed": passed,
            "provisional_only": passed,
        }
    return _audit_record(
        FORCE_QUIT_TASK,
        official_score,
        "EVAL_BUG",
        "The official evaluator requires a kill command in bash_history instead of checking whether Writer is no longer running.",
        score_lower=lower,
        score_upper=upper,
        evidence=evidence,
        bash_status="allowed",
        bash_reason="The instruction explicitly requests force quitting from the command line.",
    )


def audit_impress_notes(trace_dir, official_score):
    from pptx import Presentation

    result_path = _artifact_file(trace_dir, "164_3.pptx")
    source_path = _cache_file(
        IMPRESS_NOTES_TASK, pattern="*_164_3.pptx"
    )
    if not result_path or not source_path:
        return _audit_record(
            IMPRESS_NOTES_TASK,
            official_score,
            "NEEDS_REVIEW",
            "The speaker-notes audit is missing the result or setup presentation.",
            score_lower=0.0,
            score_upper=1.0,
            evidence={
                "result_present": bool(result_path),
                "source_present": bool(source_path),
            },
            bash_status="allowed",
            bash_reason="The instruction is outcome-based; a structured presentation edit is valid when native notes and preservation are verified.",
        )

    result = Presentation(result_path)
    source = Presentation(source_path)
    if len(result.slides) < 2 or len(source.slides) < 2:
        passed = False
        evidence = {"result_slides": len(result.slides), "source_slides": len(source.slides)}
    else:
        slide = result.slides[1]
        title = slide.shapes.title
        title_text = title.text.strip() if title is not None else ""
        title_runs = [
            run
            for paragraph in (title.text_frame.paragraphs if title is not None else [])
            for run in paragraph.runs
            if (run.text or "").strip()
        ]
        notes_text = slide.notes_slide.notes_text_frame.text.strip()
        source_visible = [
            (shape.text or "")
            for source_slide in source.slides
            for shape in source_slide.shapes
            if getattr(shape, "has_text_frame", False)
        ]
        result_visible = [
            (shape.text or "")
            for result_slide in result.slides
            for shape in result_slide.shapes
            if getattr(shape, "has_text_frame", False)
        ]
        passed = (
            title_text == "Team Members"
            and notes_text == title_text
            and bool(title_runs)
            and all(run.font.bold is True for run in title_runs)
            and result_visible == source_visible
        )
        evidence = {
            "title": title_text,
            "notes": notes_text,
            "title_run_count": len(title_runs),
            "all_title_runs_bold": bool(title_runs)
            and all(run.font.bold is True for run in title_runs),
            "visible_slide_text_preserved": result_visible == source_visible,
        }

    score = 1.0 if passed else 0.0
    return _audit_record(
        IMPRESS_NOTES_TASK,
        official_score,
        (
            "VALID_PASS"
            if passed and official_score == 1.0
            else "EVAL_BUG"
            if passed
            else "VALID_FAIL"
        ),
        (
            "The presentation contains real slide-2 speaker notes and a bold title while visible slide text is preserved; the raw comparator fails on unrelated empty-placeholder alignment."
            if passed and official_score != 1.0
            else "The requested native speaker note and title formatting are independently verified."
            if passed
            else "The result does not contain the requested native speaker note and bold title with preserved visible text."
        ),
        score_lower=score,
        score_upper=score,
        evidence=evidence,
        bash_status="allowed",
        bash_reason="The instruction is outcome-based; a native OOXML speaker-notes edit is equivalent when object identity and preservation are independently verified.",
    )


def _probe_media(path):
    completed = subprocess.run(
        [
            "/usr/bin/ffprobe",
            "-v",
            "error",
            "-show_entries",
            "stream=codec_name,codec_type,sample_rate,channels:format=duration",
            "-of",
            "json",
            str(path),
        ],
        text=True,
        capture_output=True,
        timeout=30,
    )
    if completed.returncode != 0:
        return {"error": completed.stderr.strip() or "ffprobe failed"}
    return json.loads(completed.stdout)


def audit_media_mp3(trace_dir, official_score):
    result_path = _artifact_file(trace_dir, "Baby Justin Bieber.mp3")
    source_path = _cache_file(
        MEDIA_MP3_TASK, pattern="*Baby Justin Bieber.mp4"
    )
    if not result_path or not source_path:
        return _audit_record(
            MEDIA_MP3_TASK,
            official_score,
            "NEEDS_REVIEW",
            "The media audit is missing the result MP3 or source MP4.",
            score_lower=0.0,
            score_upper=1.0,
            evidence={
                "result_present": bool(result_path),
                "source_present": bool(source_path),
            },
            bash_status="allowed",
            bash_reason="The instruction is outcome-based; general media conversion tools are allowed.",
        )

    result_probe = _probe_media(result_path)
    source_probe = _probe_media(source_path)
    result_streams = result_probe.get("streams") or []
    source_streams = source_probe.get("streams") or []
    result_audio = [
        item for item in result_streams if item.get("codec_type") == "audio"
    ]
    result_video = [
        item for item in result_streams if item.get("codec_type") == "video"
    ]
    source_audio = [
        item for item in source_streams if item.get("codec_type") == "audio"
    ]
    try:
        result_duration = float((result_probe.get("format") or {}).get("duration"))
        source_duration = float((source_probe.get("format") or {}).get("duration"))
    except (TypeError, ValueError):
        result_duration = source_duration = None
    duration_delta = (
        abs(result_duration - source_duration)
        if result_duration is not None and source_duration is not None
        else None
    )
    passed = (
        len(result_audio) == 1
        and result_audio[0].get("codec_name") == "mp3"
        and not result_video
        and bool(source_audio)
        and duration_delta is not None
        and duration_delta <= 1.0
        and result_path.stat().st_size > 0
        and isinstance(official_score, (int, float))
        and official_score >= 0.95
    )
    score = 1.0 if passed else 0.0
    return _audit_record(
        MEDIA_MP3_TASK,
        official_score,
        (
            "VALID_PASS"
            if passed and official_score == 1.0
            else "EVAL_BUG"
            if passed
            else "VALID_FAIL"
        ),
        (
            "The result is a valid MP3 with one audio stream and source-matching duration; the raw evaluator exposes an unthresholded codec-dependent similarity value as partial credit."
            if passed and official_score != 1.0
            else "The requested MP3 is structurally valid and covers the source audio duration."
            if passed
            else "The result is not a valid complete MP3 conversion of the source audio."
        ),
        score_lower=score,
        score_upper=score,
        evidence={
            "result_bytes": result_path.stat().st_size,
            "result_audio_streams": result_audio,
            "result_video_streams": result_video,
            "source_audio_streams": source_audio,
            "result_duration": result_duration,
            "source_duration": source_duration,
            "duration_delta": duration_delta,
        },
        bash_status="allowed",
        bash_reason="The instruction is outcome-based; ffmpeg conversion is a valid general-purpose channel.",
    )


def audit_task(
    task_id,
    official_score,
    trace_dir,
    *,
    as_of=None,
    container_name=None,
):
    as_of = as_of or date.today()
    if task_id == AGE_TASK:
        return audit_age(trace_dir, official_score, as_of)
    if task_id == PDF_TASK:
        return audit_pdf(trace_dir, official_score)
    if task_id == VLC_TASK:
        return audit_vlc(trace_dir, official_score)
    if task_id == SSH_TASK:
        return audit_ssh(trace_dir, official_score, container_name)
    if task_id == MOVIE_TASK:
        return audit_movie(trace_dir, official_score)
    if task_id == TERMINAL_TASK:
        return audit_terminal(trace_dir, official_score)
    if task_id == CONDA_TASK:
        return audit_conda(trace_dir, official_score, container_name)
    if task_id == FORCE_QUIT_TASK:
        return audit_force_quit(trace_dir, official_score, container_name)
    if task_id in CALC_CHART_SERIALIZATION_TASKS:
        return audit_calc_chart_serialization(
            task_id, trace_dir, official_score
        )
    if task_id == CALC_PERIOD_RATE_TASK:
        return audit_calc_period_rate(trace_dir, official_score)
    if task_id == CALC_ZOOM_TASK:
        return audit_calc_zoom(trace_dir, official_score)
    if task_id == CALC_SPARKLINE_TASK:
        return audit_calc_sparklines(trace_dir, official_score)
    if task_id == IMPRESS_EXACT_GREEN_TASK:
        return audit_impress_exact_green(trace_dir, official_score)
    if task_id == IMPRESS_NOTES_TASK:
        return audit_impress_notes(trace_dir, official_score)
    if task_id == MEDIA_MP3_TASK:
        return audit_media_mp3(trace_dir, official_score)
    site_block = _site_block_evidence(trace_dir)
    if site_block and official_score != 1.0:
        return _audit_record(
            task_id,
            official_score,
            "SETUP_BUG",
            "The target site presented an anti-bot verification barrier confirmed by the final read-only MCP observation.",
            include=False,
            evidence=site_block,
            bash_status="visible_state_required",
            bash_reason="Any channel is allowed, but the requested site state remained blocked by the environment.",
        )
    return None


def _official_status(result):
    score = result.get("score")
    if (
        result.get("setup_failure")
        or result.get("evaluation_failure")
        or result.get("error") is not None
        or result.get("truncated")
        or not isinstance(score, (int, float))
        or isinstance(score, bool)
        or not math.isfinite(score)
    ):
        return "ERR"
    if score == 1.0:
        return "PASS"
    if score > 0.0:
        return "PARTIAL"
    return "FAIL"


def _audit_validation_errors(audit):
    errors = []
    classification = audit.get("classification")
    if classification not in AUDIT_CLASSIFICATIONS:
        errors.append(f"invalid classification {classification!r}")
    if not str(audit.get("reason") or "").strip():
        errors.append("missing reason")
    evidence = audit.get("evidence")
    if not isinstance(evidence, dict) or not evidence:
        errors.append("missing evidence")
    include = audit.get("include_in_capability_denominator")
    if not isinstance(include, bool):
        errors.append("include_in_capability_denominator must be boolean")

    lower = audit.get("score_lower")
    upper = audit.get("score_upper")
    if include:
        if not (
            isinstance(lower, (int, float))
            and not isinstance(lower, bool)
            and math.isfinite(lower)
            and isinstance(upper, (int, float))
            and not isinstance(upper, bool)
            and math.isfinite(upper)
            and 0.0 <= lower <= upper <= 1.0
        ):
            errors.append(
                "included audit must have finite 0 <= lower <= upper <= 1"
            )
    elif lower is not None or upper is not None:
        errors.append("excluded audit must use null score bounds")

    audited_score = audit.get("audited_score")
    expected_score = lower if lower == upper else None
    if audited_score != expected_score:
        errors.append("audited_score is inconsistent with score bounds")

    bash = audit.get("bash_equivalence")
    if not isinstance(bash, dict):
        errors.append("missing bash_equivalence")
    else:
        if bash.get("status") not in BASH_EQUIVALENCE_STATUSES:
            errors.append(
                f"invalid bash_equivalence status {bash.get('status')!r}"
            )
        if not str(bash.get("reason") or "").strip():
            errors.append("missing bash_equivalence reason")
    return errors


def _mandatory_suspicious_pass_reasons(result):
    if _official_status(result) != "PASS":
        return []
    reasons = []
    if result.get("task_id") in KNOWN_TASKS:
        reasons.append("known_evaluator_audit")
    if result.get("declared_infeasible") or result.get("saw_infeasible"):
        reasons.append("infeasibility_sentinel")

    seconds = result.get("agent_seconds")
    if (
        isinstance(seconds, (int, float))
        and not isinstance(seconds, bool)
        and math.isfinite(seconds)
        and seconds >= SUSPICIOUS_PASS_AGENT_SECONDS
    ):
        reasons.append("high_agent_seconds")
    input_tokens = result.get("input_tokens")
    if (
        isinstance(input_tokens, int)
        and not isinstance(input_tokens, bool)
        and input_tokens >= SUSPICIOUS_PASS_INPUT_TOKENS
    ):
        reasons.append("high_input_tokens")

    tool_breakdown = result.get("tool_breakdown") or {}
    gui_breakdown = result.get("gui_action_breakdown") or {}
    semantic_mcp_mutations = sum(
        count
        for name, count in tool_breakdown.items()
        if name in MCP_MUTATION_TOOLS
        and isinstance(count, int)
        and not isinstance(count, bool)
        and count > 0
    )
    semantic_mcp_mutations += sum(
        count
        for name, count in gui_breakdown.items()
        if name not in GUI_NON_MUTATION_ACTIONS
        and isinstance(count, int)
        and not isinstance(count, bool)
        and count > 0
    )
    shell_calls = result.get("shell_calls") or 0
    if shell_calls > 0 and semantic_mcp_mutations == 0:
        reasons.append("shell_with_no_semantic_mcp_mutation")
    if (
        (result.get("tool_calls") or 0) == 0
        and shell_calls == 0
        and not result.get("file_changes")
    ):
        reasons.append("zero_recorded_actions")
    return reasons


def summarize_provisional_audited(results):
    counts = collections.Counter()
    official_statuses = collections.Counter()
    reviewed = 0
    non_pass_total = 0
    non_pass_reviewed = 0
    non_pass_unreviewed = []
    unresolved = []
    denominator = 0
    score_lower_sum = 0.0
    score_upper_sum = 0.0
    strict_pass_lower = 0
    strict_pass_upper = 0

    for result in results:
        status = _official_status(result)
        official_statuses[status] += 1
        audit = result.get("audited")
        if audit:
            reviewed += 1
            counts[audit.get("classification", "UNKNOWN")] += 1
            if audit.get("classification") == "NEEDS_REVIEW":
                unresolved.append(result.get("task_id"))
        if status != "PASS":
            non_pass_total += 1
            if audit:
                non_pass_reviewed += 1
            else:
                non_pass_unreviewed.append(result.get("task_id"))
        if audit:
            if not audit.get("include_in_capability_denominator", True):
                continue
            lower = audit.get("score_lower")
            upper = audit.get("score_upper")
        else:
            lower = result.get("score")
            upper = result.get("score")

        if not (
            isinstance(lower, (int, float))
            and not isinstance(lower, bool)
            and math.isfinite(lower)
            and isinstance(upper, (int, float))
            and not isinstance(upper, bool)
            and math.isfinite(upper)
        ):
            continue
        denominator += 1
        score_lower_sum += lower
        score_upper_sum += upper
        strict_pass_lower += lower == 1.0
        strict_pass_upper += upper == 1.0

    return {
        "reviewed": reviewed,
        "total": len(results),
        "coverage": reviewed / len(results) if results else 0.0,
        "official_statuses": dict(sorted(official_statuses.items())),
        "non_pass_total": non_pass_total,
        "non_pass_reviewed": non_pass_reviewed,
        "non_pass_coverage": (
            non_pass_reviewed / non_pass_total if non_pass_total else 1.0
        ),
        "non_pass_audit_complete": not non_pass_unreviewed,
        "non_pass_unreviewed_task_ids": sorted(
            task_id for task_id in non_pass_unreviewed if task_id
        ),
        "unresolved_review_task_ids": sorted(
            task_id for task_id in unresolved if task_id
        ),
        "classifications": dict(sorted(counts.items())),
        "capability_denominator": denominator,
        "strict_pass_lower": strict_pass_lower,
        "strict_pass_upper": strict_pass_upper,
        "score_sum_lower": score_lower_sum,
        "score_sum_upper": score_upper_sum,
    }


def _resolved_trace_root(path):
    value = Path(path)
    if not value.is_absolute():
        value = ROOT / value
    return str(value.resolve())


def _task_ids_digest(task_ids):
    payload = "\n".join(sorted(task_ids)).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def _hash_bound_file(digest, label, path):
    encoded = label.encode("utf-8")
    digest.update(len(encoded).to_bytes(8, "big"))
    digest.update(encoded)
    file_hash = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            file_hash.update(chunk)
    digest.update(file_hash.digest())


def _task_trace_dir(trace_root, task_id):
    root = Path(trace_root)
    direct = root / task_id
    if direct.is_dir():
        return direct
    hybrid = root / "hybrid" / task_id
    if hybrid.is_dir():
        return hybrid
    return direct


def _trace_evidence_digest_v2(trace_root, task_ids):
    """Legacy v2 binding for trajectories and preserved artifacts."""
    root = Path(trace_root)
    if not root.is_absolute():
        root = ROOT / root
    digest = hashlib.sha256()
    fixed_names = (
        "agent.jsonl",
        "result.json",
        "audit.json",
        "pre_agent_state.json",
        "pre_agent_screenshot.png",
    )
    for task_id in sorted(task_ids):
        task_dir = _task_trace_dir(root, task_id)
        paths = [
            task_dir / name
            for name in fixed_names
            if (task_dir / name).is_file()
        ]
        artifacts = task_dir / "artifacts"
        if artifacts.is_dir():
            paths.extend(
                path for path in artifacts.rglob("*") if path.is_file()
            )
        for path in sorted(paths):
            _hash_bound_file(
                digest, path.relative_to(root).as_posix(), path
            )
    return digest.hexdigest()


def _trace_evidence_digest(trace_root, task_ids):
    """V3 binding for trace, resource, task-config and evaluator bytes."""
    root = Path(trace_root)
    if not root.is_absolute():
        root = ROOT / root
    digest = hashlib.sha256()
    digest.update(b"osw-hybrid-trace-evidence-v3\0")
    digest.update(bytes.fromhex(_trace_evidence_digest_v2(root, task_ids)))

    for task_id in sorted(task_ids):
        task_dir = _task_trace_dir(root, task_id)
        resource = task_dir / "resource.jsonl"
        if resource.is_file():
            _hash_bound_file(
                digest,
                f"trace/{task_id}/resource.jsonl",
                resource,
            )
        if task_dir.parent.name == "hybrid":
            already_bound = {
                "agent.jsonl",
                "result.json",
                "audit.json",
                "pre_agent_state.json",
                "pre_agent_screenshot.png",
                "resource.jsonl",
            }
            for path in sorted(task_dir.rglob("*")):
                if not path.is_file() or path.name in already_bound:
                    continue
                _hash_bound_file(
                    digest,
                    f"trace/{path.relative_to(root).as_posix()}",
                    path,
                )
        configs = sorted(
            (ROOT / "OSWorld" / "evaluation_examples" / "examples").glob(
                f"*/{task_id}.json"
            )
        )
        for config in configs:
            _hash_bound_file(
                digest,
                f"task-config/{config.relative_to(ROOT).as_posix()}",
                config,
            )

    evaluator_root = ROOT / "OSWorld" / "desktop_env" / "evaluators"
    if evaluator_root.is_dir():
        for path in sorted(evaluator_root.rglob("*.py")):
            _hash_bound_file(
                digest,
                f"evaluator/{path.relative_to(ROOT).as_posix()}",
                path,
            )
    return digest.hexdigest()


def _workspace_binding_path(value):
    if not isinstance(value, str) or not value.strip():
        raise ValueError("path must be a non-empty string")
    path = Path(value)
    if not path.is_absolute():
        path = ROOT / path
    resolved = path.resolve()
    try:
        resolved.relative_to(ROOT.resolve())
    except ValueError as error:
        raise ValueError(f"path escapes workspace: {value}") from error
    return resolved


def _implementation_binding_errors(binding):
    errors = []
    if not isinstance(binding, dict):
        return ["implementation_binding must be an object"]
    image = binding.get("image")
    image_digest = binding.get("image_digest")
    if not isinstance(image, str) or not image.strip():
        errors.append("implementation_binding.image is missing")
    if (
        not isinstance(image_digest, str)
        or re.fullmatch(r"sha256:[0-9a-f]{64}", image_digest) is None
    ):
        errors.append("implementation_binding.image_digest is invalid")

    manifest_value = binding.get("manifest")
    manifest_hash = binding.get("manifest_sha256")
    try:
        manifest = _workspace_binding_path(manifest_value)
    except ValueError as error:
        errors.append(f"implementation manifest path is invalid: {error}")
        manifest = None
    if manifest is not None:
        if not manifest.is_file():
            errors.append(f"implementation manifest is missing: {manifest_value}")
        else:
            actual_hash = hashlib.sha256(manifest.read_bytes()).hexdigest()
            if manifest_hash != actual_hash:
                errors.append(
                    "implementation manifest hash mismatch: "
                    f"expected {manifest_hash}, found {actual_hash}"
                )
            try:
                payload = json.loads(manifest.read_text())
            except (json.JSONDecodeError, OSError) as error:
                errors.append(f"implementation manifest is unreadable: {error}")
            else:
                if payload.get("image") != image:
                    errors.append("implementation manifest image mismatch")
                if payload.get("image_digest") != image_digest:
                    errors.append("implementation manifest image digest mismatch")

    for path_key, hash_key in (
        ("candidate_manifest", "candidate_manifest_sha256"),
        ("run_manifest", "run_manifest_sha256"),
        ("task_list", "task_list_sha256"),
        ("result_log", "result_log_sha256"),
        ("token_log", "token_log_sha256"),
        ("judge_recovery", "judge_recovery_sha256"),
    ):
        path_value = binding.get(path_key)
        expected_hash = binding.get(hash_key)
        if path_value is None and expected_hash is None:
            continue
        if path_value is None or expected_hash is None:
            errors.append(
                f"implementation binding requires both {path_key} and {hash_key}"
            )
            continue
        try:
            path = _workspace_binding_path(path_value)
        except ValueError as error:
            errors.append(f"{path_key} path is invalid: {error}")
            continue
        if not path.is_file():
            errors.append(f"{path_key} is missing: {path_value}")
            continue
        actual_hash = hashlib.sha256(path.read_bytes()).hexdigest()
        if expected_hash != actual_hash:
            errors.append(
                f"{hash_key} mismatch: expected {expected_hash}, found {actual_hash}"
            )
    return errors


def _load_manual_reviews(paths, trace_roots):
    expected_roots = {_resolved_trace_root(path) for path in trace_roots}
    reviews = {}
    for path in paths:
        source = Path(path)
        payload = json.loads(source.read_text())
        declared_roots = {
            _resolved_trace_root(root) for root in payload.get("trace_roots", [])
        }
        if len(declared_roots) != 1 or not declared_roots.issubset(expected_roots):
            raise ValueError(
                f"Manual review {source} must be bound to exactly one requested trace root"
            )
        declared_root = next(iter(declared_roots))
        for review in payload.get("tasks", []):
            task_id = review.get("task_id")
            if not task_id:
                raise ValueError(f"Manual review {source} has a task without task_id")
            key = (declared_root, task_id)
            if key in reviews:
                raise ValueError(
                    f"Duplicate manual review for {task_id} in {declared_root}"
                )
            reviews[key] = dict(review)
    return reviews


def _load_audit_scopes(paths, trace_roots):
    expected_roots = {_resolved_trace_root(path) for path in trace_roots}
    scopes = {}
    for path in paths:
        source = Path(path)
        payload = json.loads(source.read_text())
        declared_roots = {
            _resolved_trace_root(root) for root in payload.get("trace_roots", [])
        }
        if len(declared_roots) != 1 or not declared_roots.issubset(expected_roots):
            raise ValueError(
                f"Manual review {source} must be bound to exactly one requested trace root"
            )
        scope = payload.get("audit_scope")
        if scope is None:
            continue
        declared_root = next(iter(declared_roots))
        if declared_root in scopes:
            raise ValueError(
                f"Duplicate audit scope for {declared_root}"
            )
        if not isinstance(scope, dict):
            raise ValueError(f"Manual review {source} has a non-object audit_scope")
        scopes[declared_root] = {
            **scope,
            "_manual_review_version": payload.get("version", 1),
            "_implementation_binding": payload.get("implementation_binding"),
            "_manual_review_source": str(source),
        }
    return scopes


def _final_audit_status(
    rows,
    summary,
    audit_scopes,
    trace_roots,
    manual_reviews=None,
    require_all_task_review=False,
):
    manual_reviews = manual_reviews or {}
    blockers = []
    suspicious_blockers = []
    if not summary["non_pass_audit_complete"]:
        blockers.append(
            "unreviewed non-PASS tasks: "
            + ", ".join(summary["non_pass_unreviewed_task_ids"])
        )
    if summary["unresolved_review_task_ids"]:
        blockers.append(
            "unresolved NEEDS_REVIEW tasks: "
            + ", ".join(summary["unresolved_review_task_ids"])
        )

    rows_by_root = collections.defaultdict(dict)
    invalid_audits = []
    for row in rows:
        trace_root = row.get("_audit_trace_root")
        task_id = row.get("task_id")
        if trace_root and task_id:
            rows_by_root[trace_root][task_id] = row
        if row.get("audited"):
            errors = _audit_validation_errors(row["audited"])
            if errors:
                invalid_audits.append(
                    {
                        "trace_root": trace_root,
                        "task_id": task_id,
                        "errors": errors,
                    }
                )
    for invalid in invalid_audits:
        blockers.append(
            "invalid audit record "
            f"{invalid['task_id']} in {invalid['trace_root']}: "
            + "; ".join(invalid["errors"])
        )

    suspicious_pass_ids = []
    suspicious_pass_reviews = []
    mandatory_suspicious_passes = []
    trace_completeness = []
    implementation_bindings = []
    all_task_review_missing = []
    policy_excluded_tasks = []
    for root in map(_resolved_trace_root, trace_roots):
        actual_task_ids = sorted(rows_by_root[root])
        actual_count = len(actual_task_ids)
        actual_digest = _task_ids_digest(actual_task_ids)
        drive_tasks = sorted(set(actual_task_ids) & GOOGLE_DRIVE_TASK_IDS)
        if drive_tasks:
            policy_excluded_tasks.extend(
                {"trace_root": root, "task_id": task_id}
                for task_id in drive_tasks
            )
            blockers.append(
                f"formal audit task set contains excluded Google Drive tasks "
                f"for {root}: " + ", ".join(drive_tasks)
            )
        mandatory_by_task = {}
        for task_id, row in rows_by_root[root].items():
            reasons = _mandatory_suspicious_pass_reasons(row)
            if reasons:
                mandatory_by_task[task_id] = reasons
                mandatory_suspicious_passes.append(
                    {
                        "trace_root": root,
                        "task_id": task_id,
                        "reasons": reasons,
                    }
                )
        scope = audit_scopes.get(root)
        if not scope:
            blocker = f"missing audit_scope for {root}"
            blockers.append(blocker)
            suspicious_blockers.append(blocker)
            continue
        if require_all_task_review:
            manually_reviewed = {
                task_id
                for review_root, task_id in manual_reviews
                if review_root == root
            }
            missing_manual = sorted(
                set(actual_task_ids) - manually_reviewed
            )
            if missing_manual:
                all_task_review_missing.extend(
                    {
                        "trace_root": root,
                        "task_id": task_id,
                    }
                    for task_id in missing_manual
                )
                blockers.append(
                    f"all-task manual review is incomplete for {root}: "
                    + ", ".join(missing_manual)
                )
        expected_count = scope.get("expected_task_count")
        expected_digest = scope.get("expected_task_ids_sha256")
        expected_evidence_digest = scope.get(
            "expected_trace_evidence_sha256"
        )
        actual_evidence_digest = _trace_evidence_digest(
            root, actual_task_ids
        ) if scope.get("_manual_review_version", 1) >= 3 else (
            _trace_evidence_digest_v2(root, actual_task_ids)
        )
        completeness = {
            "trace_root": root,
            "expected_task_count": expected_count,
            "actual_task_count": actual_count,
            "expected_task_ids_sha256": expected_digest,
            "actual_task_ids_sha256": actual_digest,
            "expected_trace_evidence_sha256": expected_evidence_digest,
            "actual_trace_evidence_sha256": actual_evidence_digest,
        }
        trace_completeness.append(completeness)
        if (
            not isinstance(expected_count, int)
            or isinstance(expected_count, bool)
            or expected_count <= 0
        ):
            blockers.append(
                f"expected_task_count must be a positive integer for {root}"
            )
        elif expected_count != actual_count:
            blockers.append(
                f"trace task count mismatch for {root}: "
                f"expected {expected_count}, found {actual_count}"
            )
        if (
            not isinstance(expected_digest, str)
            or len(expected_digest) != 64
        ):
            blockers.append(
                f"expected_task_ids_sha256 is missing or invalid for {root}"
            )
        elif expected_digest != actual_digest:
            blockers.append(
                f"trace task-id digest mismatch for {root}: "
                f"expected {expected_digest}, found {actual_digest}"
            )
        if scope.get("_manual_review_version", 1) >= 2:
            if (
                not isinstance(expected_evidence_digest, str)
                or len(expected_evidence_digest) != 64
            ):
                blockers.append(
                    f"expected_trace_evidence_sha256 is missing or invalid for {root}"
                )
            elif expected_evidence_digest != actual_evidence_digest:
                blockers.append(
                    f"trace evidence digest mismatch for {root}: "
                    f"expected {expected_evidence_digest}, "
                    f"found {actual_evidence_digest}"
                )
        if scope.get("_manual_review_version", 1) >= 3:
            binding_errors = _implementation_binding_errors(
                scope.get("_implementation_binding")
            )
            implementation_bindings.append(
                {
                    "trace_root": root,
                    "manual_review": scope.get("_manual_review_source"),
                    "valid": not binding_errors,
                    "errors": binding_errors,
                }
            )
            for error in binding_errors:
                blockers.append(
                    f"invalid implementation binding for {root}: {error}"
                )
        if scope.get("suspicious_pass_review") != "complete":
            blocker = f"suspicious PASS review is not complete for {root}"
            blockers.append(blocker)
            suspicious_blockers.append(blocker)
        if not str(scope.get("selection_policy") or "").strip():
            blocker = f"suspicious PASS selection_policy is missing for {root}"
            blockers.append(blocker)
            suspicious_blockers.append(blocker)
        task_ids = scope.get("suspicious_pass_task_ids")
        if not isinstance(task_ids, list):
            blocker = (
                f"suspicious_pass_task_ids must be a list for {root}"
            )
            blockers.append(blocker)
            suspicious_blockers.append(blocker)
            continue
        if any(
            not isinstance(task_id, str) or not task_id.strip()
            for task_id in task_ids
        ):
            blocker = (
                f"suspicious_pass_task_ids must contain non-empty strings for {root}"
            )
            blockers.append(blocker)
            suspicious_blockers.append(blocker)
            continue
        if len(set(task_ids)) != len(task_ids):
            blocker = f"suspicious_pass_task_ids contains duplicates for {root}"
            blockers.append(blocker)
            suspicious_blockers.append(blocker)
        missing_mandatory = sorted(set(mandatory_by_task) - set(task_ids))
        if missing_mandatory:
            blocker = (
                f"mandatory suspicious PASS tasks are missing for {root}: "
                + ", ".join(missing_mandatory)
            )
            blockers.append(blocker)
            suspicious_blockers.append(blocker)
        for task_id in task_ids:
            row = rows_by_root[root].get(task_id)
            if row is None:
                blocker = (
                    f"suspicious PASS task {task_id} is absent from {root}"
                )
                blockers.append(blocker)
                suspicious_blockers.append(blocker)
                continue
            if _official_status(row) != "PASS":
                blocker = (
                    f"suspicious PASS task {task_id} is not an official PASS"
                )
                blockers.append(blocker)
                suspicious_blockers.append(blocker)
                continue
            if not row.get("audited"):
                blocker = (
                    f"suspicious PASS task {task_id} has no adjudication"
                )
                blockers.append(blocker)
                suspicious_blockers.append(blocker)
                continue
            suspicious_pass_ids.append(task_id)
            suspicious_pass_reviews.append(
                {"trace_root": root, "task_id": task_id}
            )

    return {
        "suspicious_pass_review_complete": not suspicious_blockers,
        "suspicious_pass_task_ids": sorted(set(suspicious_pass_ids)),
        "suspicious_pass_reviews": sorted(
            suspicious_pass_reviews,
            key=lambda item: (item["trace_root"], item["task_id"]),
        ),
        "mandatory_suspicious_passes": sorted(
            mandatory_suspicious_passes,
            key=lambda item: (item["trace_root"], item["task_id"]),
        ),
        "trace_completeness": trace_completeness,
        "implementation_bindings": implementation_bindings,
        "all_task_review_required": bool(require_all_task_review),
        "all_task_review_complete": not all_task_review_missing,
        "all_task_review_missing": all_task_review_missing,
        "policy_excluded_tasks": policy_excluded_tasks,
        "invalid_audit_records": invalid_audits,
        "final_audit_ready": not blockers,
        "final_audit_blockers": blockers,
    }


def _load_v2_queue(trace_root):
    paths = []
    canonical_path = trace_root / "QUEUE.json"
    if canonical_path.is_file():
        paths.append(canonical_path)
    paths.extend(sorted(trace_root.glob("QUEUE_*.json")))
    for path in paths:
        try:
            payload = json.loads(path.read_text())
        except (OSError, json.JSONDecodeError):
            continue
        tasks = payload.get("tasks")
        if isinstance(tasks, list) and tasks:
            return payload
    return None


def _normalize_trace_result(result):
    normalized = dict(result)
    trace = normalized.get("trace_summary")
    if not isinstance(trace, dict):
        return normalized
    normalized.setdefault("agent_seconds", normalized.get("duration_sec"))
    normalized.setdefault("tool_calls", trace.get("mcp_calls"))
    normalized.setdefault("shell_calls", trace.get("shell_calls"))
    normalized.setdefault("input_tokens", trace.get("input_tokens"))
    normalized.setdefault("tool_breakdown", trace.get("tool_breakdown"))
    normalized.setdefault("last_message", normalized.get("final_message"))
    final_message = str(normalized.get("final_message") or "")
    if "TASK_INFEASIBLE" in final_message:
        normalized.setdefault("declared_infeasible", True)
        normalized.setdefault("saw_infeasible", True)
    return normalized


def _iter_trace_results(trace_root):
    seen = set()
    for result_path in sorted(trace_root.glob("*/result.json")):
        result = _normalize_trace_result(json.loads(result_path.read_text()))
        task_id = str(result.get("task_id") or result_path.parent.name)
        seen.add(task_id)
        yield result_path.parent, result

    queue = _load_v2_queue(trace_root)
    if queue is None:
        return
    state_path = trace_root / "QUEUE_STATE.json"
    try:
        queue_state = json.loads(state_path.read_text())
    except (OSError, json.JSONDecodeError):
        queue_state = {}
    failures = queue_state.get("failures") or {}
    for task in queue["tasks"]:
        task_id = str(task.get("id") or "")
        if not task_id or task_id in seen:
            continue
        task_dir = trace_root / "hybrid" / task_id
        result_path = task_dir / "result.json"
        if result_path.is_file():
            result = _normalize_trace_result(json.loads(result_path.read_text()))
        else:
            queue_key = f"{task_id}:hybrid"
            result = {
                "task_id": task_id,
                "score": None,
                "runner_failure": queue_key in failures,
                "error": failures.get(
                    queue_key,
                    "missing result for selected OSWorld V2 task",
                ),
            }
        seen.add(task_id)
        yield task_dir, result


def _load_trace_results(trace_roots, as_of, manual_reviews=None):
    manual_reviews = manual_reviews or {}
    rows = []
    for trace_root in trace_roots:
        trace_root = Path(_resolved_trace_root(trace_root))
        for task_dir, result in _iter_trace_results(trace_root):
            task_id = str(result.get("task_id") or task_dir.name)
            manual_key = (str(trace_root), task_id)
            audit = None
            if result.get("setup_failure"):
                audit = _audit_record(
                    task_id,
                    result.get("score"),
                    "SETUP_BUG",
                    "The task setup failed before the evaluated agent started.",
                    include=False,
                    evidence={
                        "error": result.get("error"),
                        "runner_returncode": result.get("runner_returncode"),
                    },
                    bash_status="needs_review",
                    bash_reason="No capability judgment is possible because the initial task state was not created.",
                )
            elif result.get("evaluation_failure"):
                audit = _audit_record(
                    task_id,
                    result.get("score"),
                    "EVAL_BUG",
                    "Evaluation failed after the agent run; no task score is valid.",
                    include=False,
                    evidence={
                        "error": result.get("error"),
                        "runner_returncode": result.get("runner_returncode"),
                    },
                    bash_status="needs_review",
                    bash_reason="The produced state cannot be adjudicated until the evaluator failure is repaired.",
                )
            elif manual_key in manual_reviews:
                audit = dict(manual_reviews[manual_key])
                audit["version"] = AUDIT_VERSION
                audit["task_id"] = task_id
                audit["official_score"] = result.get("score")
            else:
                audit = audit_task(
                    task_id,
                    result.get("score"),
                    task_dir,
                    as_of=as_of,
                )
                if audit is None and result.get("truncated"):
                    audit = _audit_record(
                        task_id,
                        result.get("score"),
                        "NEEDS_REVIEW",
                        "The agent run was truncated before a normal completion boundary.",
                        include=False,
                        evidence={
                            "agent_returncode": result.get("agent_returncode"),
                            "agent_seconds": result.get("agent_seconds"),
                            "last_message": result.get("last_message"),
                        },
                        bash_status="needs_review",
                        bash_reason="Artifact and trajectory review is required before assigning capability credit.",
                    )
                if (
                    audit is None
                    and result.get("score") == 1.0
                    and (
                        result.get("declared_infeasible")
                        or result.get("saw_infeasible")
                    )
                ):
                    audit = _audit_record(
                        task_id,
                        result.get("score"),
                        "NEEDS_REVIEW",
                        "The evaluator returned PASS after the agent declared the task infeasible.",
                        include=False,
                        evidence={
                            "last_message": result.get("last_message"),
                            "task_is_infeasible": result.get("task_is_infeasible"),
                        },
                        bash_status="needs_review",
                        bash_reason="The instruction, environment and trajectory must be reviewed before infeasibility receives capability credit.",
                    )
            row = dict(result)
            row["_audit_trace_root"] = str(trace_root)
            if audit:
                row["audited"] = audit
            rows.append(row)
    return rows


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--trace-root",
        action="append",
        required=True,
        help="May be repeated to combine fixed-subset trace roots.",
    )
    parser.add_argument("--as-of", default="")
    parser.add_argument("--output", required=True)
    parser.add_argument(
        "--manual-review",
        action="append",
        default=[],
        help="Run-bound manual audit file; may be repeated.",
    )
    parser.add_argument(
        "--require-final-audit",
        action="store_true",
        help=(
            "Exit non-zero unless every non-PASS result is adjudicated, no "
            "NEEDS_REVIEW remains, and each trace root has a completed "
            "suspicious-PASS review scope."
        ),
    )
    parser.add_argument(
        "--require-all-task-review",
        action="store_true",
        help=(
            "Require every task in every requested trace root to have a "
            "run-bound manual review record. Use this for published capability "
            "scores."
        ),
    )
    args = parser.parse_args()

    as_of = date.fromisoformat(args.as_of) if args.as_of else date.today()
    manual_reviews = _load_manual_reviews(
        args.manual_review, args.trace_root
    )
    audit_scopes = _load_audit_scopes(
        args.manual_review, args.trace_root
    )
    rows = _load_trace_results(args.trace_root, as_of, manual_reviews)
    reviewed = [
        row["audited"] for row in rows if row.get("audited") is not None
    ]
    summary = summarize_provisional_audited(rows)
    summary.update(
        _final_audit_status(
            rows,
            summary,
            audit_scopes,
            args.trace_root,
            manual_reviews=manual_reviews,
            require_all_task_review=args.require_all_task_review,
        )
    )
    payload = {
        "version": AUDIT_VERSION,
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "as_of": as_of.isoformat(),
        "trace_roots": args.trace_root,
        "manual_reviews": args.manual_review,
        "summary": summary,
        "tasks": reviewed,
    }
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(payload["summary"], ensure_ascii=False, indent=2))
    if args.require_final_audit and not summary["final_audit_ready"]:
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
