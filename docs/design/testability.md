# Testability

`internal/sys` holds every real syscall. Every other package declares its own
narrow interface over the subset it needs and fakes it in tests, so all logic is
unit-testable with no root and no VM.

Two packages declare kernel constants locally rather than importing
`golang.org/x/sys/unix` — `internal/shutdown` (reboot commands, `MS_REMOUNT`)
and `internal/switchroot` (`MS_MOVE`) — so that they build and test on a non-Linux
dev machine. Each has a `_linux_test.go` asserting the local values match `unix`
on the real target. Those assertions compile on macOS but only *run* under
`GOOS=linux`.
