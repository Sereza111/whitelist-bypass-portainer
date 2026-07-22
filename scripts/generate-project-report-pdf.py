#!/usr/bin/env python3
"""Render the project handoff Markdown into a stable, styled PDF artifact."""

from __future__ import annotations

import re
import sys
from pathlib import Path
from xml.sax.saxutils import escape

from reportlab.lib import colors
from reportlab.lib.enums import TA_CENTER, TA_LEFT
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import ParagraphStyle, getSampleStyleSheet
from reportlab.lib.units import mm
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.platypus import (
    CondPageBreak,
    PageBreak,
    Paragraph,
    Preformatted,
    SimpleDocTemplate,
    Spacer,
    Table,
    TableStyle,
)


ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "docs" / "PROJECT_REPORT_2026-07-21.md"
OUTPUT = ROOT / "output" / "pdf" / "whitelist-bypass-project-report-2026-07-21.pdf"

INK = colors.HexColor("#20191c")
MUTED = colors.HexColor("#74676b")
IVORY = colors.HexColor("#f5efe6")
PAPER = colors.HexColor("#fbf8f2")
BURGUNDY = colors.HexColor("#762d43")
BURGUNDY_DARK = colors.HexColor("#3d1422")
ROSE = colors.HexColor("#b96c7d")
GREEN = colors.HexColor("#49745e")
LINE = colors.HexColor("#d6c7c8")
CODE_BG = colors.HexColor("#eee8e1")


def register_fonts() -> None:
    fonts = Path("C:/Windows/Fonts")
    pdfmetrics.registerFont(TTFont("Body", str(fonts / "arial.ttf")))
    pdfmetrics.registerFont(TTFont("BodyBold", str(fonts / "arialbd.ttf")))
    pdfmetrics.registerFont(TTFont("Display", str(fonts / "georgia.ttf")))
    pdfmetrics.registerFont(TTFont("DisplayBold", str(fonts / "georgiab.ttf")))
    pdfmetrics.registerFont(TTFont("Code", str(fonts / "consola.ttf")))
    pdfmetrics.registerFontFamily("Body", normal="Body", bold="BodyBold")
    pdfmetrics.registerFontFamily("Display", normal="Display", bold="DisplayBold")


def styles() -> dict[str, ParagraphStyle]:
    base = getSampleStyleSheet()
    return {
        "cover_kicker": ParagraphStyle(
            "CoverKicker", parent=base["Normal"], fontName="BodyBold", fontSize=9,
            leading=12, textColor=ROSE, tracking=2.2, alignment=TA_CENTER,
            spaceAfter=9 * mm,
        ),
        "cover_title": ParagraphStyle(
            "CoverTitle", parent=base["Title"], fontName="DisplayBold", fontSize=29,
            leading=35, textColor=IVORY, alignment=TA_CENTER, spaceAfter=7 * mm,
        ),
        "cover_subtitle": ParagraphStyle(
            "CoverSubtitle", parent=base["Normal"], fontName="Body", fontSize=11,
            leading=17, textColor=colors.HexColor("#cbbdc0"), alignment=TA_CENTER,
            leftIndent=16 * mm, rightIndent=16 * mm,
        ),
        "cover_meta": ParagraphStyle(
            "CoverMeta", parent=base["Normal"], fontName="Code", fontSize=8.4,
            leading=14, textColor=colors.HexColor("#d8c8ca"), alignment=TA_CENTER,
        ),
        "h2": ParagraphStyle(
            "H2", parent=base["Heading1"], fontName="DisplayBold", fontSize=19,
            leading=23, textColor=BURGUNDY_DARK, spaceBefore=7 * mm,
            spaceAfter=3.8 * mm, keepWithNext=True,
        ),
        "h3": ParagraphStyle(
            "H3", parent=base["Heading2"], fontName="DisplayBold", fontSize=13,
            leading=17, textColor=BURGUNDY, spaceBefore=4.5 * mm,
            spaceAfter=2.2 * mm, keepWithNext=True,
        ),
        "body": ParagraphStyle(
            "Body", parent=base["BodyText"], fontName="Body", fontSize=9.3,
            leading=13.7, textColor=INK, alignment=TA_LEFT, spaceAfter=2.4 * mm,
            splitLongWords=True,
        ),
        "bullet": ParagraphStyle(
            "Bullet", parent=base["BodyText"], fontName="Body", fontSize=9.1,
            leading=13.2, textColor=INK, leftIndent=5.5 * mm,
            firstLineIndent=-3.6 * mm, bulletIndent=0.8 * mm, spaceAfter=1.2 * mm,
        ),
        "number": ParagraphStyle(
            "Number", parent=base["BodyText"], fontName="Body", fontSize=9.1,
            leading=13.2, textColor=INK, leftIndent=7 * mm,
            firstLineIndent=-5.2 * mm, spaceAfter=1.2 * mm,
        ),
        "code": ParagraphStyle(
            "Code", parent=base["Code"], fontName="Code", fontSize=7.4,
            leading=10.2, textColor=colors.HexColor("#362e31"), leftIndent=4 * mm,
            rightIndent=4 * mm, spaceBefore=1.8 * mm, spaceAfter=3.4 * mm,
            borderColor=LINE, borderWidth=0.5, borderPadding=7,
            backColor=CODE_BG,
        ),
        "table": ParagraphStyle(
            "Table", parent=base["BodyText"], fontName="Body", fontSize=7.5,
            leading=10.2, textColor=INK,
        ),
        "table_header": ParagraphStyle(
            "TableHeader", parent=base["BodyText"], fontName="BodyBold", fontSize=7.3,
            leading=9.7, textColor=IVORY,
        ),
        "small": ParagraphStyle(
            "Small", parent=base["BodyText"], fontName="Body", fontSize=7.8,
            leading=11, textColor=MUTED,
        ),
    }


def inline_markup(value: str) -> str:
    tokens: list[tuple[str, str]] = []

    def stash(pattern: str, replacement) -> None:
        nonlocal value

        def repl(match: re.Match[str]) -> str:
            token = f"@@TOKEN{len(tokens)}@@"
            tokens.append((token, replacement(match)))
            return token

        value = re.sub(pattern, repl, value)

    stash(r"\[([^\]]+)\]\((https?://[^)]+)\)", lambda m: f'<link href="{escape(m.group(2))}" color="#762d43">{escape(m.group(1))}</link>')
    stash(r"<(https?://[^>]+)>", lambda m: f'<link href="{escape(m.group(1))}" color="#762d43">{escape(m.group(1))}</link>')
    stash(r"`([^`]+)`", lambda m: f'<font name="Code" size="8">{escape(m.group(1))}</font>')
    stash(r"\*\*([^*]+)\*\*", lambda m: f"<b>{escape(m.group(1))}</b>")
    value = escape(value)
    for token, rendered in tokens:
        value = value.replace(token, rendered)
    return value


def wrap_code(code: str, width: int = 92) -> str:
    wrapped: list[str] = []
    for line in code.splitlines() or [""]:
        if len(line) <= width:
            wrapped.append(line)
            continue
        indent = len(line) - len(line.lstrip())
        prefix = line[:indent]
        rest = line[indent:]
        while len(prefix) + len(rest) > width:
            cut = rest.rfind(" ", 0, max(12, width - len(prefix)))
            if cut < 12:
                cut = width - len(prefix)
            wrapped.append(prefix + rest[:cut])
            rest = rest[cut:].lstrip()
            prefix = " " * min(indent + 2, 10)
        wrapped.append(prefix + rest)
    return "\n".join(wrapped)


def parse_table(lines: list[str], style_map: dict[str, ParagraphStyle], available: float) -> Table:
    rows = [[cell.strip() for cell in line.strip().strip("|").split("|")] for line in lines]
    if len(rows) > 1 and all(re.fullmatch(r":?-{3,}:?", cell.replace(" ", "")) for cell in rows[1]):
        rows.pop(1)
    rendered = []
    for row_index, row in enumerate(rows):
        rendered.append([
            Paragraph(inline_markup(cell), style_map["table_header" if row_index == 0 else "table"])
            for cell in row
        ])
    columns = max(len(row) for row in rendered)
    if columns == 2:
        widths = [available * 0.31, available * 0.69]
    elif columns == 5:
        widths = [available * 0.18] + [available * 0.205] * 4
    else:
        widths = [available / columns] * columns
    table = Table(rendered, colWidths=widths, repeatRows=1, hAlign="LEFT")
    table.setStyle(TableStyle([
        ("BACKGROUND", (0, 0), (-1, 0), BURGUNDY_DARK),
        ("TEXTCOLOR", (0, 0), (-1, 0), IVORY),
        ("GRID", (0, 0), (-1, -1), 0.35, LINE),
        ("VALIGN", (0, 0), (-1, -1), "TOP"),
        ("LEFTPADDING", (0, 0), (-1, -1), 5),
        ("RIGHTPADDING", (0, 0), (-1, -1), 5),
        ("TOPPADDING", (0, 0), (-1, -1), 5),
        ("BOTTOMPADDING", (0, 0), (-1, -1), 5),
        ("ROWBACKGROUNDS", (0, 1), (-1, -1), [colors.white, colors.HexColor("#f4efea")]),
    ]))
    return table


def markdown_story(source: str, style_map: dict[str, ParagraphStyle], available: float) -> list:
    lines = source.splitlines()
    start = next((i for i, line in enumerate(lines) if line.startswith("## 1.")), 0)
    lines = lines[start:]
    story: list = []
    paragraph: list[str] = []
    code: list[str] = []
    in_code = False
    table_lines: list[str] = []

    def flush_paragraph() -> None:
        if paragraph:
            text = " ".join(part.strip() for part in paragraph).strip()
            if text:
                story.append(Paragraph(inline_markup(text), style_map["body"]))
            paragraph.clear()

    def flush_table() -> None:
        if table_lines:
            story.append(parse_table(table_lines, style_map, available))
            story.append(Spacer(1, 3.2 * mm))
            table_lines.clear()

    for line in lines:
        if line.startswith("```"):
            flush_paragraph()
            flush_table()
            if in_code:
                story.append(Preformatted(wrap_code("\n".join(code)), style_map["code"])); code.clear()
            in_code = not in_code
            continue
        if in_code:
            code.append(line)
            continue
        if line.startswith("|") and line.rstrip().endswith("|"):
            flush_paragraph()
            table_lines.append(line)
            continue
        flush_table()
        if not line.strip():
            flush_paragraph()
            continue
        if line.startswith("## "):
            flush_paragraph()
            story.append(CondPageBreak(46 * mm))
            story.append(Paragraph(inline_markup(line[3:]), style_map["h2"]))
            continue
        if line.startswith("### "):
            flush_paragraph()
            story.append(CondPageBreak(28 * mm))
            story.append(Paragraph(inline_markup(line[4:]), style_map["h3"]))
            continue
        if line.startswith("- "):
            flush_paragraph()
            story.append(Paragraph(inline_markup(line[2:]), style_map["bullet"], bulletText="•"))
            continue
        numbered = re.match(r"^(\d+)\.\s+(.*)$", line)
        if numbered:
            flush_paragraph()
            story.append(Paragraph(
                f'<font name="BodyBold" color="#762d43">{numbered.group(1)}.</font> {inline_markup(numbered.group(2))}',
                style_map["number"],
            ))
            continue
        paragraph.append(line)
    flush_paragraph()
    flush_table()
    return story


def draw_cover(canvas, doc) -> None:
    width, height = A4
    canvas.saveState()
    canvas.setFillColor(colors.HexColor("#100b0f"))
    canvas.rect(0, 0, width, height, stroke=0, fill=1)
    canvas.setStrokeColor(BURGUNDY)
    canvas.setLineWidth(1.2)
    canvas.rect(13 * mm, 13 * mm, width - 26 * mm, height - 26 * mm, stroke=1, fill=0)
    canvas.setStrokeColor(colors.HexColor("#4a2732"))
    canvas.setLineWidth(0.35)
    canvas.rect(17 * mm, 17 * mm, width - 34 * mm, height - 34 * mm, stroke=1, fill=0)
    canvas.setFillColor(BURGUNDY)
    canvas.circle(width / 2, height - 50 * mm, 8 * mm, stroke=0, fill=1)
    canvas.setFillColor(colors.HexColor("#160d12"))
    canvas.circle(width / 2, height - 50 * mm, 5.2 * mm, stroke=0, fill=1)
    canvas.setStrokeColor(ROSE)
    canvas.line(width / 2 - 13 * mm, height - 50 * mm, width / 2 - 8.5 * mm, height - 50 * mm)
    canvas.line(width / 2 + 8.5 * mm, height - 50 * mm, width / 2 + 13 * mm, height - 50 * mm)
    canvas.restoreState()


def draw_body(canvas, doc) -> None:
    width, height = A4
    canvas.saveState()
    canvas.setFillColor(PAPER)
    canvas.rect(0, 0, width, height, stroke=0, fill=1)
    canvas.setStrokeColor(BURGUNDY)
    canvas.setLineWidth(0.65)
    canvas.line(19 * mm, height - 15 * mm, width - 19 * mm, height - 15 * mm)
    canvas.setFont("BodyBold", 7.2)
    canvas.setFillColor(BURGUNDY_DARK)
    canvas.drawString(19 * mm, height - 11.8 * mm, "WHITELIST BYPASS · PROJECT HANDOFF")
    canvas.setFont("Body", 7.2)
    canvas.setFillColor(MUTED)
    canvas.drawRightString(width - 19 * mm, height - 11.8 * mm, "v0.5.0-alpha.9 candidate · 22.07.2026")
    canvas.setStrokeColor(LINE)
    canvas.line(19 * mm, 14 * mm, width - 19 * mm, 14 * mm)
    canvas.setFont("Code", 7.2)
    canvas.setFillColor(MUTED)
    canvas.drawString(19 * mm, 9.6 * mm, "Sereza111/whitelist-bypass-portainer")
    canvas.drawRightString(width - 19 * mm, 9.6 * mm, f"{doc.page}")
    canvas.restoreState()


def build(source_path: Path = SOURCE, output_path: Path = OUTPUT) -> Path:
    register_fonts()
    style_map = styles()
    output_path.parent.mkdir(parents=True, exist_ok=True)
    doc = SimpleDocTemplate(
        str(output_path), pagesize=A4, rightMargin=19 * mm, leftMargin=19 * mm,
        topMargin=22 * mm, bottomMargin=19 * mm,
        title="Whitelist Bypass: отчёт о проделанной работе",
        author="Sereza111 / Codex",
        subject="Архитектура, релизы, deployment, security и AI handoff",
    )
    story = [
        Spacer(1, 48 * mm),
        Paragraph("ENGINEERING HANDOFF · RELEASE ALPHA.9 CANDIDATE", style_map["cover_kicker"]),
        Paragraph("Whitelist Bypass", style_map["cover_title"]),
        Paragraph(
            "Отчёт о проделанной работе, текущая архитектура, эксплуатация и контекст для следующего ИИ",
            style_map["cover_subtitle"],
        ),
        Spacer(1, 25 * mm),
        Table([["RELEASE", "COMMIT", "DATE"], ["v0.5.0-alpha.9 candidate", "pending", "22.07.2026"]],
              colWidths=[48 * mm, 48 * mm, 48 * mm], hAlign="CENTER",
              style=TableStyle([
                  ("FONTNAME", (0, 0), (-1, 0), "BodyBold"),
                  ("FONTNAME", (0, 1), (-1, 1), "Code"),
                  ("FONTSIZE", (0, 0), (-1, 0), 7),
                  ("FONTSIZE", (0, 1), (-1, 1), 8.4),
                  ("TEXTCOLOR", (0, 0), (-1, 0), ROSE),
                  ("TEXTCOLOR", (0, 1), (-1, 1), IVORY),
                  ("ALIGN", (0, 0), (-1, -1), "CENTER"),
                  ("LINEABOVE", (0, 0), (-1, 0), 0.5, BURGUNDY),
                  ("LINEBELOW", (0, 1), (-1, 1), 0.5, BURGUNDY),
                  ("TOPPADDING", (0, 0), (-1, -1), 6),
                  ("BOTTOMPADDING", (0, 0), (-1, -1), 6),
              ])),
        Spacer(1, 36 * mm),
        Paragraph("Рабочая alpha-версия для измеряемых полевых тестов", style_map["cover_meta"]),
        PageBreak(),
    ]
    story.extend(markdown_story(source_path.read_text(encoding="utf-8"), style_map, A4[0] - 38 * mm))
    doc.build(story, onFirstPage=draw_cover, onLaterPages=draw_body)
    return output_path


if __name__ == "__main__":
    source = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else SOURCE
    output = Path(sys.argv[2]).resolve() if len(sys.argv) > 2 else OUTPUT
    print(build(source, output))
