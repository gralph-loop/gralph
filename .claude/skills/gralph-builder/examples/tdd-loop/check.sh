#!/usr/bin/env sh
# Stand-in for `go test` / `pytest`. Exit 0 iff the implementation is complete.
grep -q "DONE" src/impl.txt 2>/dev/null
