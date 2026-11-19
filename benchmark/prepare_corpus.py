"""Extract benchmark office/PDF files to UTF-8 Markdown for the existing ingestion service."""

import argparse
import json
import shutil
import sys
import tempfile
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parent
DATASETS = ROOT / "datasets"
TEXT_EXTENSIONS = {".txt", ".md", ".markdown"}
SUPPORTED_EXTENSIONS = TEXT_EXTENSIONS | {".pdf", ".docx", ".pptx", ".xlsx"}


class PreparationError(ValueError):
    pass


def extract_plain_text(path: Path) -> tuple[str, dict[str, int]]:
    try:
        text = path.read_text(encoding="utf-8-sig").strip()
    except UnicodeDecodeError as error:
        raise PreparationError(f"{path.name}: expected a UTF-8 text file: {error}") from error
    return text, {}


def extract_pdf(path: Path) -> tuple[str, dict[str, int]]:
    from pypdf import PdfReader

    reader = PdfReader(path)
    pages = []
    empty_pages = 0
    for index, page in enumerate(reader.pages, start=1):
        text = (page.extract_text() or "").strip()
        if not text:
            empty_pages += 1
            continue
        pages.append(f"## Page {index}\n\n{text}")
    if not pages:
        raise PreparationError(f"{path.name}: no selectable text found; OCR is not supported")
    return "\n\n".join(pages), {"pages": len(reader.pages), "empty_pages": empty_pages}


def extract_docx(path: Path) -> tuple[str, dict[str, int]]:
    from docx import Document
    from docx.table import Table
    from docx.text.paragraph import Paragraph

    document = Document(path)
    sections = []
    paragraph_count = 0
    table_count = 0
    for child in document.element.body.iterchildren():
        if child.tag.endswith("}p"):
            paragraph = Paragraph(child, document)
            text = paragraph.text.strip()
            if text:
                sections.append(text)
                paragraph_count += 1
        elif child.tag.endswith("}tbl"):
            table = Table(child, document)
            rows = [
                " | ".join(cell.text.replace("\n", " / ").strip() for cell in row.cells)
                for row in table.rows
            ]
            rows = [row for row in rows if row.strip(" |")]
            if rows:
                sections.append("\n".join(rows))
                table_count += 1

    for section in document.sections:
        for label, region in (("Header", section.header), ("Footer", section.footer)):
            text = "\n".join(paragraph.text.strip() for paragraph in region.paragraphs if paragraph.text.strip())
            if text:
                sections.append(f"{label}: {text}")
    if not sections:
        raise PreparationError(f"{path.name}: no paragraph or table text found")
    return "\n\n".join(sections), {"paragraphs": paragraph_count, "tables": table_count}


def shape_text(shape: Any) -> list[str]:
    from pptx.enum.shapes import MSO_SHAPE_TYPE

    if shape.has_table:
        return [
            " | ".join(cell.text.strip().replace("\n", " / ") for cell in row.cells)
            for row in shape.table.rows
        ]
    if shape.has_text_frame:
        text = "\n".join(paragraph.text for paragraph in shape.text_frame.paragraphs).strip()
        return [text] if text else []
    if shape.shape_type == MSO_SHAPE_TYPE.GROUP:
        return [text for child in shape.shapes for text in shape_text(child)]
    return []


def extract_pptx(path: Path) -> tuple[str, dict[str, int]]:
    from pptx import Presentation

    presentation = Presentation(path)
    slides = []
    text_shapes = 0
    for index, slide in enumerate(presentation.slides, start=1):
        texts = [
            text
            for shape in slide.shapes
            for text in shape_text(shape)
            if text.strip()
        ]
        if texts:
            text_shapes += len(texts)
            slides.append(f"## Slide {index}\n\n" + "\n\n".join(texts))
    if not slides:
        raise PreparationError(f"{path.name}: no selectable slide text found")
    return "\n\n".join(slides), {"slides": len(presentation.slides), "text_shapes": text_shapes}


def cell_text(cell: Any, cached_cell: Any) -> str:
    value = cached_cell.value
    formula = cell.value
    if isinstance(formula, str) and formula.startswith("="):
        if value is None:
            return formula
        return f"{value} [formula: {formula}]"
    if value is None:
        value = formula
    if value is None:
        return ""
    return str(value).strip()


def extract_xlsx(path: Path) -> tuple[str, dict[str, int]]:
    from openpyxl import load_workbook

    formulas = load_workbook(path, read_only=True, data_only=False)
    values = load_workbook(path, read_only=True, data_only=True)
    sections = []
    nonempty_cells = 0
    try:
        for worksheet in formulas.worksheets:
            value_sheet = values[worksheet.title]
            rows = []
            value_rows = value_sheet.iter_rows()
            for row, cached_row in zip(worksheet.iter_rows(), value_rows):
                entries = []
                for cell, cached_cell in zip(row, cached_row):
                    value = cell_text(cell, cached_cell)
                    if value:
                        entries.append(f"{cell.coordinate}: {value}")
                        nonempty_cells += 1
                if entries:
                    rows.append(" | ".join(entries))
            if rows:
                sections.append(f"## Sheet: {worksheet.title}\n\n" + "\n".join(rows))
    finally:
        formulas.close()
        values.close()
    if not sections:
        raise PreparationError(f"{path.name}: workbook contains no non-empty cells")
    return "\n\n".join(sections), {"sheets": len(sections), "nonempty_cells": nonempty_cells}


def extract_document(path: Path) -> tuple[str, dict[str, int]]:
    extension = path.suffix.lower()
    if extension in TEXT_EXTENSIONS:
        return extract_plain_text(path)
    if extension == ".pdf":
        return extract_pdf(path)
    if extension == ".docx":
        return extract_docx(path)
    if extension == ".pptx":
        return extract_pptx(path)
    if extension == ".xlsx":
        return extract_xlsx(path)
    raise PreparationError(f"{path.name}: unsupported file format {extension or '[no extension]'}")


def resolve_dataset(name: str) -> Path:
    root = DATASETS.resolve()
    dataset = (DATASETS / name).resolve()
    if dataset.parent != root or not dataset.is_dir():
        raise PreparationError(f"unknown dataset {name!r}; expected a directory under {root}")
    return dataset


def prepare(dataset: Path, output: Path) -> dict[str, Any]:
    source_corpus = dataset / "corpus"
    if not source_corpus.is_dir():
        raise PreparationError(f"{dataset.name}: corpus directory is missing")
    if not (source_corpus / "aliases.json").is_file():
        raise PreparationError(f"{dataset.name}: corpus/aliases.json is missing")

    sources = sorted(
        path for path in source_corpus.rglob("*")
        if path.is_file() and path.name != "aliases.json"
    )
    if not sources:
        raise PreparationError(f"{dataset.name}: corpus contains no source documents")
    unsupported = sorted({
        path.suffix.lower() or "[no extension]"
        for path in sources
        if path.suffix.lower() not in SUPPORTED_EXTENSIONS
    })
    if unsupported:
        raise PreparationError(f"{dataset.name}: unsupported source formats: {unsupported}")

    output = output.resolve()
    if output == dataset.resolve() or dataset.resolve() in output.parents:
        raise PreparationError("output directory must not be inside the source dataset")
    if output.exists():
        raise PreparationError(f"output directory already exists: {output}")
    output.parent.mkdir(parents=True, exist_ok=True)

    files = []
    with tempfile.TemporaryDirectory(prefix=f".{dataset.name}-prepare-", dir=output.parent) as temp:
        staging = Path(temp) / "prepared"
        prepared_corpus = staging / "corpus"
        prepared_corpus.mkdir(parents=True)
        shutil.copyfile(source_corpus / "aliases.json", prepared_corpus / "aliases.json")

        for source in sources:
            relative_source = source.relative_to(source_corpus)
            text, details = extract_document(source)
            if not text.strip():
                raise PreparationError(f"{relative_source}: extraction returned empty text")
            relative_output = (
                relative_source
                if source.suffix.lower() in TEXT_EXTENSIONS
                else Path(f"{relative_source.as_posix()}.md")
            )
            prepared_file = prepared_corpus / relative_output
            prepared_file.parent.mkdir(parents=True, exist_ok=True)
            title = relative_source.name
            content = f"# Source: {title}\n\n{text.strip()}\n"
            prepared_file.write_text(content, encoding="utf-8")
            files.append({
                "source_file": relative_source.as_posix(),
                "prepared_file": relative_output.as_posix(),
                "source_extension": source.suffix.lower(),
                "extracted_characters": len(text),
                **details,
            })

        manifest = {
            "dataset": dataset.name,
            "source_corpus": "corpus",
            "prepared_corpus": "corpus",
            "files": files,
        }
        (staging / "manifest.json").write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        staging.rename(output)
    return manifest


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("dataset", help="dataset directory under benchmark/datasets")
    parser.add_argument(
        "--output",
        type=Path,
        help="destination (default: ingestion-service/out/benchmark/<dataset>)",
    )
    args = parser.parse_args()

    try:
        dataset = resolve_dataset(args.dataset)
        output = args.output or ROOT.parent / "ingestion-service" / "out" / "benchmark" / dataset.name
        manifest = prepare(dataset, output)
    except (OSError, PreparationError, ImportError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1

    totals = sum(item["extracted_characters"] for item in manifest["files"])
    print(
        f"PASS {manifest['dataset']}: prepared {len(manifest['files'])} documents "
        f"({totals} extracted characters) at {output.resolve()}"
    )
    print(f"Source mapping: {output.resolve() / 'manifest.json'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
