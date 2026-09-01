# Document Runtime third-party notices

The signed Document Runtime package includes the exact component versions in
`requirements.lock` and an SPDX 2.3 SBOM generated at release time. The release
pipeline must stage the corresponding license texts with the self-contained
Python, LibreOffice, PDFium, and Tesseract distributions before signing.

Key upstream licenses:

- `python-pptx`, `python-docx`, `openpyxl`, `pypdf`, `pdfplumber`, `pypdfium2`,
  `reportlab`, `Pillow`, and `defusedxml`: retain each distribution's bundled
  license file and metadata in the release stage.
- LibreOffice: MPL-2.0/LGPL-3.0-or-later, including bundled third-party notices.
- Tesseract OCR: Apache-2.0; language data retains its upstream license notice.
- PDFium: BSD-style license plus bundled third-party notices.

This source notice is not a substitute for the release package's full license
directory. `build-document-runtime.mjs` signs every staged notice and binary.
