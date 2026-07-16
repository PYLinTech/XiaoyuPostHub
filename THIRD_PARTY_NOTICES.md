# Third-party notices

## Flyfish Viewer

The file preview capability includes **Flyfish Viewer** through
`@file-viewer/react-legacy-full` (the Flyfish Viewer ecosystem, historically
published under `@flyfish-group/file-viewer`).

- Source: https://github.com/flyfish-dev/file-viewer
- License: Apache License 2.0
- License copy shipped with the application:
  `frontend/public/third-party-licenses/flyfish-viewer-APACHE-2.0.txt`

No Flyfish Viewer source files are modified in this repository; the package is
integrated as a version-locked npm dependency. During the frontend build, its
version-matched runtime assets are copied from the installed package directly
into `frontend/build/libs/file-viewer` and redistributed unchanged; they are
not stored in this source repository.
