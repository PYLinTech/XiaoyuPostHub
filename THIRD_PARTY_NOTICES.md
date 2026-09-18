# Third-party notices

## Flyfish Viewer

The file preview capability includes **Flyfish Viewer** through
`@file-viewer/react-legacy` and the on-demand renderer packages
`@file-viewer/renderer-image`, `@file-viewer/renderer-text`,
`@file-viewer/renderer-word`, `@file-viewer/renderer-spreadsheet`,
`@file-viewer/renderer-presentation` and `@file-viewer/renderer-pdf` (the
Flyfish Viewer ecosystem, historically published under
`@flyfish-group/file-viewer`).

- Source: https://github.com/flyfish-dev/file-viewer
- License: Apache License 2.0
- License copy shipped with the application:
  `frontend/public/third-party-licenses/flyfish-viewer-APACHE-2.0.txt`

No Flyfish Viewer source files are modified in this repository; the packages
are integrated as version-locked npm dependencies. During the frontend build,
the version-matched runtime assets required by the renderers above are copied
from the installed packages directly into `frontend/build/libs/file-viewer`
and redistributed unchanged; they are not stored in this source repository.
