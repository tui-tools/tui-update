#!/bin/bash
# Backend smoke test for tui-update, run inside a lab guest.
#
# The contract (see tui-tools/tui-lab): this script runs on the guest as the
# unprivileged lab user, escalates with `sudo -n` only, prints a short PASS/FAIL
# table and exits non-zero if anything failed. The binary under test is at
# $TUI_LAB_BIN (default: tui-update on PATH).
#
# What it proves is that the tool reads the machine's *real* package manager
# and agrees with the machine's own tooling — not that a fake renders. The lab
# already covers --version and a --demo frame; this covers the backend.
#
# Three kinds of machine are asserted, one per manager:
#
#   pacman   Arch and Omarchy Server. `checkupdates` when pacman-contrib is
#            there, and the Omarchy update wrapper when the machine has one.
#   apt      Debian and Ubuntu cloud images.
#   dnf      Fedora and RHEL, dnf4 and dnf5.
#
# And one thing is asserted on every one of them: `--check` is a read that an
# ordinary user can make. It must not refresh the manager's metadata, because
# that is a privileged write to a root-owned cache, and on a machine where
# `sudo -n` does not answer it would simply fail.
set -uo pipefail

bin="${TUI_LAB_BIN:-tui-update}"
# TOOL is the manifest name, which is what a compatibility result is keyed on.
TOOL=tui-update
pass=0
fail=0

# check runs one assertion. It takes a label, a command and a grep pattern the
# command's output must match. Output is captured so a failure can show it.
check() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# check_absent is the inverse of a grep assertion: the command must succeed and
# its output must NOT contain the pattern. It is what proves something did not
# happen, which is most of what this suite is about.
check_absent() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && ! grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# check_report asserts on the one --check report already captured, rather than
# running the tool again. `dnf check-update` alone takes seconds on a machine
# with many repositories, and this suite would otherwise spend minutes
# re-reading a state that cannot have changed.
check_report() {
  local label="$1" pattern="$2"
  if grep -qE "$pattern" <<<"$report"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s\n' "$label"
    grep -E '^  "' <<<"$report" | sed 's/^/      | /' | head -12
    fail=$((fail + 1))
  fi
}

# check_report_absent is its inverse.
check_report_absent() {
  local label="$1" pattern="$2"
  if ! grep -qE "$pattern" <<<"$report"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s\n' "$label"
    grep -E "$pattern" <<<"$report" | sed 's/^/      | /' | head -6
    fail=$((fail + 1))
  fi
}

# --- compatibility evidence -------------------------------------------------
#
# The manifest's `tested` list is generated, not claimed: it is rebuilt from
# compat/results.jsonl by tui-kit/tools/compat-sync.py, and this is where a
# line of that file comes from. The version recorded is the one the tool itself
# probed, read back out of --check, so it describes the machine that really ran
# the suite rather than what the tester assumed was installed.
#
# The line is printed behind a `compat-result:` prefix so it survives the trip
# out of the guest through the lab's per-VM log, and appended to
# $TUI_COMPAT_RESULTS as well for a run outside the lab.
record_compat() {
  local report="$1" outcome="$2" backend version distro today block
  block=$(sed -n '/"compat": {/,/^  }/p' <<<"$report")
  backend=$(sed -n 's/.*"backend": "\([^"]*\)".*/\1/p' <<<"$block" | head -1)
  version=$(sed -n 's/.*"version": "\([^"]*\)".*/\1/p' <<<"$block" | head -1)
  if [[ -z $backend || -z $version ]]; then
    echo "      no version was probed, so no compatibility result is recorded"
    return
  fi

  distro=$(. /etc/os-release && echo "${ID}-${VERSION_ID:-rolling}")
  today=$(date -u +%Y-%m-%d)
  local line
  line=$(printf '{"backend":"%s","date":"%s","distro":"%s","result":"%s","suite":"smoke","tool":"%s","version":"%s"}' \
    "$backend" "$today" "$distro" "$outcome" "$TOOL" "$version")

  printf 'compat-result: %s\n' "$line"
  if [[ -n ${TUI_COMPAT_RESULTS:-} ]]; then
    printf '%s\n' "$line" >>"$TUI_COMPAT_RESULTS"
  fi
}

# json_field pulls one top-level scalar out of the --check report. The report
# is indented two spaces at the top level, so anchoring on that keeps a nested
# field of the same name out of the answer.
json_field() {
  sed -n "s/^  \"$1\": \(.*\),\{0,1\}$/\1/p" <<<"$2" | head -1 | tr -d '",'
}

echo "--- tui-update smoke on $(. /etc/os-release && echo "$PRETTY_NAME")"

# Which manager this machine really runs, decided the way a person would: the
# binary the distribution ships to upgrade itself.
distro_id=$(. /etc/os-release && echo "$ID")
case "$distro_id" in
  arch | archarm | omarchy | omarchy-server | endeavouros | manjaro | cachyos)
    manager=pacman
    ;;
  debian | ubuntu | raspbian | linuxmint | pop | devuan) manager=apt ;;
  fedora | rhel | centos | rocky | almalinux | ol) manager=dnf ;;
  *)
    if command -v pacman >/dev/null; then
      manager=pacman
    elif command -v apt >/dev/null; then
      manager=apt
    elif command -v dnf >/dev/null; then
      manager=dnf
    else
      echo "FAIL  no supported package manager on this machine"
      exit 1
    fi
    ;;
esac
echo "      distro=$distro_id manager=$manager"

if ! command -v "$manager" >/dev/null; then
  echo "FAIL  $distro_id should run $manager, but the binary is missing"
  exit 1
fi

# Whether this user can escalate without a prompt. Several assertions below
# only hold one way or the other, and none of them may hang waiting for a
# password.
if sudo -n true 2>/dev/null; then
  sudo_ok=yes
else
  sudo_ok=no
fi
echo "      sudo -n: $sudo_ok"

# 1. The read path works at all, as the plain lab user, and names the manager
#    it drove. This is itself the assertion that reading needs no privilege.
report=$("$bin" --check 2>/dev/null)
status=$?
if [[ $status -eq 0 ]] && grep -q "\"manager\": \"$manager\"" <<<"$report"; then
  printf 'PASS  check reads the updates unprivileged\n'
  pass=$((pass + 1))
else
  printf 'FAIL  check reads the updates unprivileged (exit %d)\n' "$status"
  sed 's/^/      | /' <<<"$report" | head -12
  fail=$((fail + 1))
fi

# 2. `pending` is an integer. The history and timer screens can be empty on a
#    given machine, but a count that came back null or missing means the read
#    path did not run.
pending=$(json_field pending "$report")
if [[ $pending =~ ^[0-9]+$ ]]; then
  printf 'PASS  pending is an integer (%s)\n' "$pending"
  pass=$((pass + 1))
else
  printf 'FAIL  pending is %s, want an integer\n' "${pending:-missing}"
  fail=$((fail + 1))
fi

# 3. The count matches what the manager itself says. This is the real parser
#    test: a tool that fetched the output but failed to parse it reports zero.
case "$manager" in
  pacman)
    # checkupdates comes from pacman-contrib and builds a private copy of the
    # sync database under fakeroot, so it is only usable when fakeroot is
    # installed too — Omarchy Server 4.0.1 ships one without the other. The
    # comparison has to be made against the source the tool would really use.
    if command -v checkupdates >/dev/null && command -v fakeroot >/dev/null; then
      expected=$(checkupdates 2>/dev/null | grep -cE ' -> ')
      source="checkupdates"
    else
      expected=$(pacman -Qu 2>/dev/null | grep -cE ' -> ')
      source="pacman -Qu"
    fi
    ;;
  apt)
    expected=$(apt list --upgradable 2>/dev/null |
      grep -cE '\[upgradable from: ')
    source="apt list --upgradable"
    ;;
  dnf)
    # check-update exits 100 when there are updates, so its status is not the
    # verdict; the table it printed is.
    expected=$(dnf check-update -q --cacheonly 2>/dev/null |
      grep -cE '^[^ ]+\.[^ ]+ +[^ ]+ +[^ ]+$')
    source="dnf check-update"
    ;;
esac
if [[ "$pending" == "$expected" ]]; then
  printf 'PASS  pending matches `%s` (%s)\n' "$source" "$expected"
  pass=$((pass + 1))
else
  printf 'FAIL  pending is %s, `%s` says %s\n' "$pending" "$source" "$expected"
  fail=$((fail + 1))
fi

# 4. The pending list was actually read. A count of zero is a legitimate
#    answer on a freshly updated machine; a count of zero because the command
#    failed is not, and the report says which. This is what a missing fakeroot
#    on Omarchy Server 4.0.1 used to turn into an empty screen.
check_report_absent "the pending list was read, not merely reported as zero" \
  '"pendingError"'

# 5. The restart classification is one of the three words, always. An empty
#    one would mean the classifier neither ran nor fell back.
check_report "the restart class is one of none/services/reboot" \
  '"restart": "(none|services|reboot)"'

# 6. The snapshot answer agrees with the machine. A snapper root configuration
#    is the whole condition, and it is checkable from the shell.
if [[ -e /etc/snapper/configs/root ]] && command -v snapper >/dev/null; then
  check "snapshot support is reported on a machine that has it" \
    "$bin --check" \
    '"snapshot": true'
  check "the snapshot configuration is named" \
    "$bin --check" \
    '"snapshotConfig": "root"'
else
  check "no snapshot is claimed on a machine without a snapper root config" \
    "$bin --check" \
    '"snapshot": false'
fi

# 7. The timer state agrees with systemd, for the unit this manager ships.
case "$manager" in
  pacman) unit=omarchy-server-update.timer ;;
  apt) unit=apt-daily-upgrade.timer ;;
  dnf) unit=dnf-automatic.timer ;;
esac
if grep -q "\"unit\": \"$unit\"" <<<"$report"; then
  systemd_state=$(systemctl is-enabled "$unit" 2>&1 | head -1)
  check "the state of $unit matches systemctl ($systemd_state)" \
    "$bin --check" \
    "\"state\": \"$systemd_state\""
else
  echo "SKIP  this machine declares no $unit to compare"
fi

# 8. The security count is only ever non-zero where the manager publishes the
#    metadata. On pacman a security flag would be an invention.
if [[ "$manager" == "pacman" ]]; then
  check "pacman claims no security metadata it does not have" \
    "$bin --check" \
    '"security": 0'
fi

# 8b. The hold answer agrees with the machine. It is a read like every other:
#    `apt-mark showhold` and `dnf versionlock list` both answer unprivileged
#    from local state, and a machine that cannot hold a package at all — no
#    versionlock plugin, or pacman — must say so rather than offering a key
#    that would fail.
case "$manager" in
  pacman)
    check "pacman claims no hold it cannot place" \
      "$bin --check" \
      '"canHold": false'
    ;;
  apt)
    check "apt reports that a package can be held" \
      "$bin --check" \
      '"canHold": true'
    expected_holds=$(apt-mark showhold 2>/dev/null | grep -c . || true)
    holds=$(json_field holds "$report")
    # Only the held packages that are also upgradable are on the list, so the
    # tool's count is a subset of apt-mark's rather than equal to it.
    if [[ $holds =~ ^[0-9]+$ ]] && ((holds <= expected_holds)); then
      printf 'PASS  holds (%s) is within `apt-mark showhold` (%s)\n' \
        "$holds" "$expected_holds"
      pass=$((pass + 1))
    else
      printf 'FAIL  holds is %s, `apt-mark showhold` lists %s\n' \
        "${holds:-missing}" "$expected_holds"
      fail=$((fail + 1))
    fi
    ;;
  dnf)
    # The plugin decides, and the tool must agree with whether it is there.
    if dnf versionlock list -q --cacheonly >/dev/null 2>&1; then
      want=true
    else
      want=false
    fi
    check "the hold answer matches whether versionlock is installed ($want)" \
      "$bin --check" \
      "\"canHold\": $want"
    ;;
esac

# 9. --check must not refresh the manager's metadata. That is a privileged
#    write to a root-owned cache, and it is the one thing that would make the
#    read path unusable as an ordinary user.
#
#    It is asserted on the cache's own mtime: a refresh rewrites it, and a
#    read that only consults it does not. On a machine where `sudo -n` works
#    the tool could refresh and get away with it, which is exactly why this is
#    checked there too.
case "$manager" in
  pacman) cache=/var/lib/pacman/sync ;;
  apt) cache=/var/lib/apt/lists ;;
  dnf) cache=/var/cache/libdnf5 ;;
esac
if [[ -e $cache ]]; then
  before=$(stat -c %Y "$cache" 2>/dev/null)
  $bin --check >/dev/null 2>&1
  after=$(stat -c %Y "$cache" 2>/dev/null)
  if [[ "$before" == "$after" ]]; then
    printf 'PASS  --check did not refresh %s\n' "$cache"
    pass=$((pass + 1))
  else
    printf 'FAIL  --check rewrote %s (%s -> %s)\n' "$cache" "$before" "$after"
    fail=$((fail + 1))
  fi
else
  echo "SKIP  $cache does not exist on this machine"
fi

# 10. --check must never build or run a mutation, so it can never ask for a
#    password. A prompt in its output means an escalation was attempted on a
#    path that has no business escalating interactively.
check_absent "--check never prompted for a password" \
  "$bin --check" \
  '\[sudo\] password for'

# 11. And it changes nothing: the pending list is identical afterwards.
case "$manager" in
  pacman) list_cmd="pacman -Qu" ;;
  apt) list_cmd="apt list --upgradable" ;;
  dnf) list_cmd="dnf check-update -q --cacheonly" ;;
esac
before=$(eval "$list_cmd" 2>/dev/null)
$bin --check >/dev/null 2>&1
after=$(eval "$list_cmd" 2>/dev/null)
if [[ "$before" == "$after" ]]; then
  printf 'PASS  --check left the pending list untouched\n'
  pass=$((pass + 1))
else
  printf 'FAIL  --check changed the pending list\n'
  diff <(echo "$before") <(echo "$after") | sed 's/^/      | /' | head -12
  fail=$((fail + 1))
fi

# 12. Per-manager facts worth pinning, since each one is a claim the README
#     makes on that distribution's behalf.
case "$manager" in
  pacman)
    if command -v omarchy-server-update >/dev/null; then
      check "the Omarchy update wrapper is noticed" \
        "$bin --check" \
        'omarchy-server-update'
    fi
    # A machine built by installing into a chroot and then cleaned — which is
    # how the Omarchy Server cloud image is made — has no pacman.log until its
    # first upgrade. That is an empty history, not a broken read, so it is a
    # skip rather than a failure.
    if [[ -e /var/log/pacman.log ]]; then
      check "the pacman log is readable, so the history screen has something" \
        "test -r /var/log/pacman.log && echo readable" \
        'readable'
    else
      echo "SKIP  this machine has no /var/log/pacman.log yet"
    fi
    ;;
  apt)
    if [[ -e /var/run/reboot-required ]]; then
      check "Debian's reboot-required flag is honoured" \
        "$bin --check" \
        '"rebootRequired": true'
    else
      check_absent "no reboot is claimed without the flag or a kernel update" \
        "$bin --check | grep -v linux-image" \
        '"rebootRequired": true'
    fi
    check "the apt history log is readable" \
      "test -r /var/log/apt/history.log && echo readable" \
      'readable'
    ;;
  dnf)
    # `needs-restarting -r` is the reboot verdict, and the tool must agree
    # with it. It exits 1 when a reboot is needed, so the status is the
    # answer rather than a failure.
    if command -v needs-restarting >/dev/null; then
      if needs-restarting -r >/dev/null 2>&1; then
        want=false
      else
        want=true
      fi
      check "the reboot verdict matches \`needs-restarting -r\` ($want)" \
        "$bin --check" \
        "\"rebootRequired\": $want"
    else
      echo "SKIP  needs-restarting is not installed (dnf-plugins-core)"
    fi
    # The history screen reads `dnf history list`, which answers from the
    # local database and needs no privilege. No pipe here: a `| head` would
    # kill dnf with SIGPIPE and the exit status would be about that.
    check "the dnf history is readable unprivileged" \
      "dnf history list -q" \
      '^ *[0-9]+ '
    ;;
esac

# --- the report block ------------------------------------------------------
#
# --report is read-only and unprivileged, so it is smoked without sudo: a user
# who cannot escalate is exactly the one who most needs to be able to file a
# usable bug. What is asserted is that it agrees with the manager this machine
# should be driving, that it still answers under --demo, and that it keeps its
# privacy promise — the block goes into a public issue, so a home path or the
# host name appearing in it is a bug, not a cosmetic detail.
check "report names the detected manager" \
  "$bin --report" \
  "^backend: $manager"

check "report says the run was live" \
  "$bin --report" \
  '^mode: live$'

check "report works in demo mode too" \
  "$bin --demo --report" \
  '^backend: demo$'

check "and says so on the mode line" \
  "$bin --demo --report" \
  '^mode: demo'

# The distro and kernel lines are excluded from the host-name search rather
# than from the promise: they are built from /etc/os-release and from uname's
# release and machine fields, never from its nodename, and on a guest called
# "fedora" or "ubuntu" — which is most of them — the host name is a substring
# of the distribution's own. Everything else in the block is searched.
check "report leaks neither a home path nor the host name" \
  "$bin --report | grep -vE '^(distro|kernel): ' | grep -cE '/home/|$(uname -n)' || true" \
  '^0$'

if [[ $fail -eq 0 ]]; then
  record_compat "$report" pass
else
  record_compat "$report" fail
fi

echo "--- tui-update: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
