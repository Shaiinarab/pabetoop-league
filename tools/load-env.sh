#!/bin/sh
# tools/load-env.sh — load KEY=VALUE pairs from .env into the environment.
#
# SOURCE this file; do not execute it:
#
#     . tools/load-env.sh [path/to/.env]     # defaults to ./.env
#
# Why not just `. ./.env`? Because a .env value may contain spaces and non-ASCII
# text — `SITE_NAME=Riverside FC` — and sourcing the file directly makes the shell
# try to *execute* `FC` as a command. The variable then silently ends up truncated
# to "Riverside", which looks like a branding bug rather than an env bug.
# This parser reads the file as data instead, so those values survive intact.
#
# Rules — deliberately matching `docker compose`'s `env_file:` so the Docker path
# and the from-source path behave the same way:
#   * blank lines and lines starting with # are skipped
#   * surrounding single or double quotes are stripped if present
#   * values may contain spaces and any UTF-8 text (including Persian)
#   * a CRLF line ending is tolerated (a .env edited on Windows won't poison values)
#   * an ALREADY-SET environment variable WINS — explicit env overrides .env,
#     exactly as docker compose's `environment:` block overrides `env_file:`
#   * a missing .env is not an error
#   * a leading `export ` is tolerated
#
# Note on quotes: this strips surrounding quotes, whereas docker compose's
# `env_file:` historically passes them through literally. The .env.example this
# ships with is unquoted throughout, so both paths agree as long as you don't add
# quotes of your own.

# Where the .env lives. Callers may pass it either way:
#
#     . tools/load-env.sh /path/to/.env      # works in bash; dash drops the argument
#     ENV_FILE=/path/to/.env . tools/load-env.sh   # works in every POSIX shell — preferred
#
# dash's `.` ignores extra arguments, so a bare `$1` is not portable. env.sh prefers
# $ENV_FILE and falls back to `./.env` relative to the caller's CWD, so callers should
# cd to the repo root (or set ENV_FILE) first.
env_file="${ENV_FILE:-${1:-.env}}"

[ -f "$env_file" ] || return 0

while IFS= read -r line || [ -n "$line" ]; do
    line=$(printf '%s' "$line" | tr -d '\r')

    case "$line" in
        '' | \#*) continue ;;
    esac
    case "$line" in
        *=*) ;;
        *) continue ;;
    esac

    key=${line%%=*}
    val=${line#*=}

    # tolerate `export KEY=value`
    case "$key" in
        export[[:space:]]*) key=${key#export} ;;
        export)             continue ;;
    esac

    # normalise the key; a key may never contain whitespace
    key=$(printf '%s' "$key" | tr -d '[:space:]')
    [ -n "$key" ] || continue

    case "$val" in
        \"*\") val=${val#\"}; val=${val%\"} ;;
        \'*\') val=${val#\'}; val=${val%\'} ;;
    esac

    # explicit environment wins over .env
    if ! printenv "$key" >/dev/null 2>&1; then
        export "$key=$val"
    fi
done < "$env_file"

unset env_file line key val
