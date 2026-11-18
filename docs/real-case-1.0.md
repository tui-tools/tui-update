# Real case for 1.0: holding a package, upgrading and rebooting an Ubuntu server

Point 5 of the family's [stability bar](https://github.com/tui-tools/tui-kit/blob/main/docs/stability.md):
the tool has driven a real machine end to end for its main purpose, and what
that surfaced was fixed or filed.

## The machine, plainly

A lab virtual machine, not a production server: Ubuntu 24.04.5 LTS from the
stock cloud image (apt 2.8.3, needrestart installed), booted by
[tui-lab](https://github.com/tui-tools/tui-lab) under KVM on the project's own
server, with the updates the image had fallen behind on: 50 pending, 38 of them
security fixes, including a kernel and libc. The tool ran as the ordinary user
`lab` over SSH, escalating with `sudo -n`. The build was `main` on 2026-11-18
with this release's contract changes.

The same build also ran read-only on the project's own server, a Fedora 44
machine in daily use: `tui-update --check` exited 0 with `backend: dnf` and
the pending and security counts. Nothing was changed on that machine, and its
package state is not published here.

## What was done, end to end

1. Opened: `updates: 50 pending   security: 38   restart: reboot`, with the
   kernel packages marked `·k` and libc `·c`.
2. `/cloud-init`, then `H`: `apt-mark hold cloud-init`, explained as the dpkg
   selection every apt front end honours. `apt-mark showhold` printed
   `cloud-init`.
3. `enter`: the plan. Restart class `reboot`, decided by `needrestart -b`; the
   two commands in order (`apt-get update`, then the non-interactive
   `apt-get -y … upgrade`); no snapshot, because snapper has no root
   configuration on this machine; and apt's own dry run, which listed
   `cloud-init` and the kernel metapackages as kept back and `snapd` as
   deferred by phasing.
4. `U`, `y`: the upgrade ran with nothing asked behind the screen, with
   needrestart's report on what was left on old libraries in the output, and
   the screen ended on `upgrade finished` and `R reboot`.
5. `R`, `y`: `systemctl reboot`, behind its own dialog. After the reboot,
   `/var/run/reboot-required` was gone, `cloud-init` was still held, and
   `--check` printed `apt 6 4 reboot false 1` (backend, pending, security,
   restart class, reboot required, holds): the held package, the kept-back
   kernel metapackages and the phased one.

## What it surfaced

- On apt in `upgrade` mode, the plan counted 50 packages and named the kernel
  packages as the reason for the reboot, while apt kept those back (and the
  held and phased ones); afterwards only `libc6` was in
  `/var/run/reboot-required.pkgs`. Filed as
  [#35](https://github.com/tui-tools/tui-update/issues/35). The reboot
  verdict was right on this machine, because of libc.
- For the freeze, `--check` names the package manager `backend` like every
  other tool, and `--demo-no-versionlock` is a value of `--demo-machine`.

## What it did not cover

dnf and pacman upgrades were not applied on a lab machine in this round (the
smoke test reads both, and the dnf and pacman package upgrade of the tools
themselves ran on them); the `dist-upgrade` and `security` modes, the
snapshot step and the timers screen were not exercised.
