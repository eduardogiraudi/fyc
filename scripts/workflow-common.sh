#!/bin/bash

workflow_error() {
  echo "Error: $*" >&2
  exit 1
}

workflow_edit() {
  local editor
  editor=$(git var GIT_EDITOR) || return 1
  # Git editor settings may include arguments, e.g. "code --wait".
  sh -c "$editor \"\$@\"" editor "$1"
}
