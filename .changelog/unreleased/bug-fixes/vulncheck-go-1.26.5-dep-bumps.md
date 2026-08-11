- Fix `govulncheck` failures: bump Go from 1.26.4 to 1.26.5 (GO-2026-5856),
  bump `google.golang.org/grpc` to v1.82.1 (GO-2026-6061), bump
  `github.com/go-git/go-git/v5` to v5.19.2 and `github.com/go-git/go-billy/v5`
  to v5.9.0 (GO-2026-5074, GO-2026-5105, GO-2026-5490, GO-2026-5496,
  GO-2026-5597), and remove the unused `crypto/armor` package, which was the
  only user of the unmaintained `golang.org/x/crypto/openpgp` (GO-2026-5932).
