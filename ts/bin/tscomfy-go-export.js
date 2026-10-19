#!/usr/bin/env bun
// The bin of @tscomfy/go: a committed file, so a package manager links it before dist/ is built.
// It runs the built command, the same module instance the workflows import.
// oxlint-disable-next-line import/no-unassigned-import -- the command runs on import
import "../dist/main.js";
