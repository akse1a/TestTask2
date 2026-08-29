#!/usr/bin/env python3
"""Собрать REPORT.md в печатный HTML (reports/report.html).

Мини-конвертер markdown-подмножества (stdlib only): заголовки h1–h4, таблицы,
списки, **жирный**, `код`, --- , абзацы. Статус-эмодзи заменяются текстовыми
бейджами (в печатном документе эмодзи не нужны). Дальше HTML печатается в PDF
через reports/../scratchpad/render.js (системный Chrome).
"""
import html
import re
import sys

ACCENT = "#1f6feb"

BADGES = {
    "✅": '<span class="badge ok">выполнено</span>',
    "⚠️": '<span class="badge warn">частично</span>',
    "❌": '<span class="badge no">нет</span>',
}


def inline(text):
    # экранируем, потом возвращаем разметку
    text = html.escape(text)
    text = re.sub(r"`([^`]+)`", r"<code>\1</code>", text)
    text = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", text)
    for e, b in BADGES.items():
        text = text.replace(e, b)
    return text


def convert(md):
    lines = md.split("\n")
    out = []
    i = 0
    n = len(lines)
    headings = []  # (level, id, title) для оглавления

    def flush_para(buf):
        if buf:
            out.append("<p>" + inline(" ".join(buf).strip()) + "</p>")
            buf.clear()

    para = []
    slug = 0
    while i < n:
        line = lines[i]

        # таблица: строка с | и следующая строка-разделитель
        if line.strip().startswith("|") and i + 1 < n and re.match(r"^\s*\|[\s:|-]+\|\s*$", lines[i + 1]):
            flush_para(para)
            header = [c.strip() for c in line.strip().strip("|").split("|")]
            i += 2
            rows = []
            while i < n and lines[i].strip().startswith("|"):
                rows.append([c.strip() for c in lines[i].strip().strip("|").split("|")])
                i += 1
            t = ['<table><thead><tr>']
            t += [f"<th>{inline(h)}</th>" for h in header]
            t.append("</tr></thead><tbody>")
            for r in rows:
                t.append("<tr>" + "".join(f"<td>{inline(c)}</td>" for c in r) + "</tr>")
            t.append("</tbody></table>")
            out.append("".join(t))
            continue

        m = re.match(r"^(#{1,4})\s+(.*)$", line)
        if m:
            flush_para(para)
            level = len(m.group(1))
            title = m.group(2).strip()
            slug += 1
            hid = f"h{slug}"
            if level == 2:
                headings.append((level, hid, re.sub(r"[`*]", "", title)))
            out.append(f'<h{level} id="{hid}">{inline(title)}</h{level}>')
            i += 1
            continue

        if re.match(r"^---+\s*$", line):
            flush_para(para)
            out.append('<hr>')
            i += 1
            continue

        def is_break(s):
            return (s.strip() == "" or re.match(r"^#{1,4}\s", s) or re.match(r"^---+\s*$", s)
                    or s.strip().startswith("|"))

        if re.match(r"^\s*[-*]\s+", line):
            flush_para(para)
            items = []
            while i < n and re.match(r"^\s*[-*]\s+", lines[i]):
                cur = re.sub(r"^\s*[-*]\s+", "", lines[i])
                i += 1
                # приклеиваем перенесённые строки одного пункта
                while i < n and not is_break(lines[i]) and not re.match(r"^\s*(?:[-*]|\d+\.)\s+", lines[i]):
                    cur += " " + lines[i].strip()
                    i += 1
                items.append(cur)
            out.append("<ul>" + "".join(f"<li>{inline(x)}</li>" for x in items) + "</ul>")
            continue

        if re.match(r"^\s*\d+\.\s+", line):
            flush_para(para)
            items = []
            while i < n and re.match(r"^\s*\d+\.\s+", lines[i]):
                cur = re.sub(r"^\s*\d+\.\s+", "", lines[i])
                i += 1
                while i < n and not is_break(lines[i]) and not re.match(r"^\s*(?:[-*]|\d+\.)\s+", lines[i]):
                    cur += " " + lines[i].strip()
                    i += 1
                items.append(cur)
            out.append("<ol>" + "".join(f"<li>{inline(x)}</li>" for x in items) + "</ol>")
            continue

        if line.strip() == "":
            flush_para(para)
            i += 1
            continue

        para.append(line)
        i += 1

    flush_para(para)
    return "\n".join(out), headings


def build(md_path, html_path):
    with open(md_path, encoding="utf-8") as f:
        md = f.read()

    # первый h1 и следующий за ним абзац-мета уводим в титул
    body_html, headings = convert(md)
    toc = "".join(
        f'<li><a href="#{hid}">{html.escape(title)}</a></li>'
        for level, hid, title in headings
    )

    doc = f"""<!doctype html>
<html lang="ru"><head><meta charset="utf-8">
<title>Отчёт по ДЗ №2 · Платёжный мини-API</title>
<style>
  :root {{ --accent: {ACCENT}; --ink:#1a1d21; --muted:#5c6470; --line:#e3e6ea; }}
  * {{ box-sizing: border-box; }}
  html {{ -webkit-print-color-adjust: exact; print-color-adjust: exact; }}
  body {{ font-family: Georgia, "Times New Roman", serif; color: var(--ink);
         font-size: 10.5pt; line-height: 1.5; margin: 0; }}
  h1,h2,h3,h4 {{ font-family: "Helvetica Neue", Arial, sans-serif; line-height:1.25;
         color: var(--ink); }}
  h2 {{ font-size: 15pt; margin: 22px 0 8px; padding-bottom:4px;
        border-bottom: 2px solid var(--accent); }}
  h3 {{ font-size: 12pt; margin: 16px 0 6px; color:#2b3038; }}
  h4 {{ font-size: 10.5pt; margin: 12px 0 4px; }}
  p {{ margin: 6px 0; }}
  ul,ol {{ margin: 6px 0 6px 18px; padding:0; }}
  li {{ margin: 2px 0; }}
  code {{ font-family: "SFMono-Regular", Consolas, monospace; font-size: 9pt;
          background:#f2f4f7; padding:1px 4px; border-radius:3px; }}
  hr {{ border:0; border-top:1px solid var(--line); margin: 16px 0; }}
  a {{ color: var(--accent); text-decoration: none; }}
  table {{ border-collapse: collapse; width:100%; margin:10px 0; font-size:9.2pt;
           font-family:"Helvetica Neue", Arial, sans-serif; }}
  th {{ background: var(--accent); color:#fff; text-align:left; padding:5px 8px;
        font-weight:600; }}
  td {{ border-bottom:1px solid var(--line); padding:5px 8px; vertical-align:top; }}
  tr:nth-child(even) td {{ background:#f7f9fb; }}
  .badge {{ font-family:"Helvetica Neue",Arial,sans-serif; font-size:8pt; font-weight:700;
            padding:1px 7px; border-radius:10px; color:#fff; white-space:nowrap; }}
  .badge.ok {{ background:#1a7f37; }}
  .badge.warn {{ background:#9a6700; }}
  .badge.no {{ background:#8a8f98; }}
  /* Титул */
  .cover {{ height: 251mm; display:flex; flex-direction:column; justify-content:center;
            page-break-after: always; }}
  .cover .kicker {{ font-family:"Helvetica Neue",Arial,sans-serif; letter-spacing:2px;
            text-transform:uppercase; color:var(--accent); font-size:10pt; font-weight:700; }}
  .cover h1 {{ font-family:"Helvetica Neue",Arial,sans-serif; font-size:30pt; margin:10px 0 6px;
            line-height:1.1; }}
  .cover .sub {{ color:var(--muted); font-size:13pt; }}
  .cover .meta {{ margin-top:28px; font-family:"Helvetica Neue",Arial,sans-serif; font-size:10.5pt;
            color:#2b3038; }}
  .cover .rule {{ width:64px; height:4px; background:var(--accent); margin:18px 0; }}
  /* Оглавление */
  .toc {{ page-break-after: always; }}
  .toc h2 {{ border:0; }}
  .toc ol {{ font-family:"Helvetica Neue",Arial,sans-serif; font-size:11pt; list-style:none;
             margin-left:0; }}
  .toc li {{ padding:5px 0; border-bottom:1px dotted var(--line); }}
  h2 {{ page-break-after: avoid; }}
  table, ul, ol {{ page-break-inside: avoid; }}
</style></head>
<body>
  <section class="cover">
    <div class="kicker">Домашнее задание №2 · Финтех</div>
    <h1>Платёжный мини-API<br>с идемпотентностью</h1>
    <div class="rule"></div>
    <div class="sub">Вариант C1 · собрано несколькими агентами по спецификации</div>
    <div class="meta">
      Автор: Elnar Aksaitov &nbsp;·&nbsp; Формат: соло<br>
      Дата: 29 августа 2026 &nbsp;·&nbsp; Стек: Go (стандартная библиотека)
    </div>
  </section>
  <section class="toc">
    <h2>Оглавление</h2>
    <ol>{toc}</ol>
  </section>
  <main>
  {body_html}
  </main>
</body></html>"""

    with open(html_path, "w", encoding="utf-8") as f:
        f.write(doc)
    print("wrote", html_path)


if __name__ == "__main__":
    md = sys.argv[1] if len(sys.argv) > 1 else "REPORT.md"
    outp = sys.argv[2] if len(sys.argv) > 2 else "reports/report.html"
    build(md, outp)
