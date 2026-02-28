# Codebase review: proposed tasks

## 1) Typo fix task
**Title:** Align outdated project/binary naming in README commands.

**Problem:** The README uses legacy naming (`outline-ws-lb`) in `cd`, build output, and run commands, while the repository/module entrypoint path is `outline-cli-ws`.

**Task:** Replace stale `outline-ws-lb` references in command snippets with consistent project naming (repo directory and resulting binary name), and verify snippets run as-is from a fresh clone.

---

## 2) Bug fix task
**Title:** Respect explicit disabling of active probes in config.

**Problem:** If both `probe.enable_tcp` and `probe.enable_udp` are set to `false`, loader logic force-enables both, so users cannot explicitly disable probing.

**Task:** Distinguish “not specified” from explicit `false` (e.g., pointer bools or additional flags), and only apply default-enabling when values are absent.

---

## 3) Comment/doc mismatch task
**Title:** Fix example config `weight` type mismatch.

**Problem:** Example config sets float weights (`1.0`, `0.5`) but code expects integer `weight` (`int`), which can fail YAML unmarshal and mislead users.

**Task:** Either change examples to integer weights or change `UpstreamConfig.Weight` to a compatible numeric type (with validation), and update docs accordingly.

---

## 4) Test improvement task
**Title:** Expand `normalizeHostPort` tests for negative/edge IPv6 cases.

**Problem:** Existing tests cover a few positive cases, but do not protect against regressions for tricky inputs (e.g., unbracketed IPv6 without numeric port suffix, non-numeric suffix, malformed bracket cases).

**Task:** Add table-driven cases asserting no accidental normalization/mangling for edge inputs, plus explicit expected behavior for invalid port ranges.
