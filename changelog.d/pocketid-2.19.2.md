- Pocket ID 2.19.2 is supported and tested; it takes 2.19.1's place in the
  acceptance matrix (the tested range is now 2.14.0 through 2.19.2). 2.19.2 is
  a dependency-only patch of 2.19.1 and needs no configuration change.
- Built with `golang.org/x/net` 0.62.0, which fixes GO-2026-6866 and
  GO-2026-6860. The provider did not call the affected code.
