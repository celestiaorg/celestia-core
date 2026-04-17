- `[consensus]` Fix flakiness in `TestByzantinePrevoteEquivocation` by
  waiting for the full peer mesh before the byzantine node fires
  conflicting prevotes and replacing the 120s `time.After` deadline with
  two-stage `require.Eventually` polling on the evidence pool and the
  block store. Supersedes #2810 and #2850. Closes #2200.
