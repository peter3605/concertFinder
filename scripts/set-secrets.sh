#!/usr/bin/env bash
# Fill the SecureString parameters that Terraform provisions as REPLACE_ME.
# Run from your own machine, with credentials that can write Parameter Store.
#
# Values are typed at a hidden prompt and handed to the CLI through a
# mode-600 temp file, never on a command line. That matters more than it
# looks: a value in argv lands in shell history, in `ps` for any other user on
# the machine, and -- if you were tempted to push it through SSM instead -- in
# send-command history for ~30 days. Going through a file keeps it out of all
# three, and CloudTrail redacts SecureString values.
#
# Safe to re-run. Press Enter at any prompt to leave that parameter alone.
set -euo pipefail

PARAM_PATH=${PARAM_PATH:-/concertfinder}
PLACEHOLDER=REPLACE_ME

: "${AWS_PROFILE:?set AWS_PROFILE (e.g. export AWS_PROFILE=budgetr)}"
: "${AWS_REGION:=us-east-1}"
export AWS_REGION

command -v aws >/dev/null || { echo "aws cli not found" >&2; exit 1; }

tmp=$(mktemp "${TMPDIR:-/tmp}/cf-secret.XXXXXX")
chmod 600 "$tmp"
cleanup() {
    # Overwrite before unlinking. rm alone leaves the bytes on disk until the
    # blocks are reused, and this file held a live credential.
    [ -f "$tmp" ] && dd if=/dev/urandom of="$tmp" bs=1024 count=4 conv=notrunc status=none 2>/dev/null || true
    rm -f "$tmp"
}
trap cleanup EXIT

put() {
    local key=$1 value=$2
    # printf, not echo: no trailing newline. A newline inside DATABASE_URL
    # would be carried into .env and break the connection string in a way
    # that reads as a bad password.
    printf '%s' "$value" > "$tmp"
    aws ssm put-parameter \
        --name "$PARAM_PATH/$key" \
        --type SecureString \
        --value "file://$tmp" \
        --overwrite \
        --output text --query Version > /dev/null
    printf '  \033[32mset\033[0m %s\n' "$key"
}

prompt() {
    local key=$1 hint=$2 value
    printf '\n%s\n' "$key"
    [ -n "$hint" ] && printf '  %s\n' "$hint"
    printf '  value (Enter to skip): '
    read -rs value
    printf '\n'
    if [ -z "$value" ]; then
        printf '  \033[33mskipped\033[0m\n'
        return
    fi
    put "$key" "$value"
}

echo "Writing SecureString parameters under $PARAM_PATH"
echo "Profile: $AWS_PROFILE   Region: $AWS_REGION"

prompt DATABASE_URL \
    "Neon DIRECT endpoint (no -pooler), ending in ?sslmode=require"
prompt SPOTIFY_CLIENT_ID \
    "From developer.spotify.com/dashboard. Not actually secret under PKCE."
prompt TICKETMASTER_API_KEY \
    "From developer.ticketmaster.com"

# ENCRYPTION_KEY is not interchangeable with a fresh one: it decrypts the
# Spotify refresh tokens already in the users table. Setting a new value means
# existing users' background scans fail until each logs in once, which
# re-encrypts their token under the new key. That is cheap at two users and
# expensive at two hundred, so the prompt says so rather than assuming.
printf '\nENCRYPTION_KEY\n'
printf '  Decrypts refresh tokens already stored. A NEW key invalidates them:\n'
printf '  existing users must log in once more before their scans work again.\n'
printf '  [g] generate a new one   [p] paste existing   [Enter] skip: '
read -r choice
case "$choice" in
    g|G)
        put ENCRYPTION_KEY "$(openssl rand -hex 32)"
        printf '  \033[33mnote\033[0m existing users must log in again\n'
        ;;
    p|P)
        printf '  value: '
        read -rs v; printf '\n'
        [ -n "$v" ] && put ENCRYPTION_KEY "$v" || printf '  \033[33mskipped\033[0m\n'
        ;;
    *) printf '  \033[33mskipped\033[0m\n' ;;
esac

# Optional integrations. render-env.sh only rejects the literal REPLACE_ME, so
# a single space is a legitimate "configured as empty" -- the Go side treats an
# empty key as "this tier is off", which is what an unset integration means.
printf '\nOptional integrations (Enter to skip, or "-" to mark as unused):\n'
for key in SONGKICK_API_KEY BRAVE_SEARCH_API_KEY; do
    printf '\n%s\n  value: ' "$key"
    read -rs value; printf '\n'
    case "$value" in
        '')  printf '  \033[33mskipped\033[0m\n' ;;
        '-') put "$key" " " ;;
        *)   put "$key" "$value" ;;
    esac
done

# APNs. Multi-line PEM, so this cannot use the single-line `read` above: a
# pasted key would be truncated at the first newline and fail at send time as
# an unparseable p8, long after the deploy reported success.
#
# It is in operator_secrets, so Terraform creates it holding the sentinel and
# render-env.sh fails the next deploy until it is set or explicitly marked
# unused. That is deliberate — a half-configured APNs set is the state that
# wires up and silently drops every notification — but it does mean a
# deployment with no Apple account has to mark it "-" once.
printf '\nAPNS_P8_KEY\n'
printf '  The .p8 file from developer.apple.com (Keys), PEM contents.\n'
printf '  [f] read from a file   [-] mark as unused   [Enter] skip: '
read -r choice
case "$choice" in
    f|F)
        printf '  path to .p8: '
        read -r p8path
        # Expand a leading ~ that the shell does not expand inside a variable.
        case "$p8path" in "~/"*) p8path="$HOME/${p8path#\~/}" ;; esac
        if [ -r "$p8path" ]; then
            # Escape the newlines. A PEM is multi-line and every consumer
            # downstream of here is line-oriented: render-env.sh reads
            # parameters with `--output text` and a `while IFS=$'\t' read`
            # loop, so a stored newline ends the record -- APNS_P8_KEY would
            # arrive holding only "-----BEGIN PRIVATE KEY-----" and each
            # remaining PEM line would become its own junk `<base64>=` entry
            # in .env. Even past that, docker compose env_file has no
            # multi-line syntax. push.parseP8 unescapes before pem.Decode,
            # and .env.example documents this as the storage format.
            esc=""
            while IFS= read -r line || [ -n "$line" ]; do
                esc="$esc$line\\n"
            done < "$p8path"
            put APNS_P8_KEY "$esc"
            unset esc line
        else
            printf '  \033[31mcannot read %s\033[0m\n' "$p8path"
        fi
        ;;
    -)  put APNS_P8_KEY " " ;;
    *)  printf '  \033[33mskipped\033[0m\n' ;;
esac

# The check that matters. A parameter still holding the sentinel fails the next
# deploy in render-env.sh, so surface it now rather than from a workflow log.
printf '\n--- parameters still unset ---\n'
remaining=$(aws ssm get-parameters-by-path --path "$PARAM_PATH" --with-decryption \
    --query "Parameters[?Value=='$PLACEHOLDER'].Name" --output text)
if [ -n "$remaining" ]; then
    printf '\033[31m%s\033[0m\n' "$remaining" | tr '\t' '\n'
    printf '\nThe next deploy will refuse to render .env until these are set.\n'
    exit 1
fi
printf '\033[32mall set\033[0m — safe to merge the PR and deploy\n'
