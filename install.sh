#!/bin/sh
# Installs waggle on this machine, macOS or Linux, or upgrades it: run it again, it brings the clone to the wanted
# version, rebuilds the image and restarts what changed. The first time, waggle setup asks the configuration
# (README, ADR-0003, decision 6).
#
#   gh api repos/apiar-dev/apiar-waggle/contents/install.sh -H 'Accept: application/vnd.github.raw+json' | sh
#
# The repository is private: gh, signed in to an account that can read it, fetches this script, then the clone.
#
#   WAGGLE_REF       the branch or tag to install, main by default;
#   WAGGLE_HOME      the clone, ~/.local/share/waggle by default;
#   WAGGLE_BIN       the directory the command is linked into, ~/.local/bin by default;
#   WAGGLE_CONFIG    the configuration file, ~/.config/waggle/config.yaml by default, as for the command;
#   WAGGLE_SETUP     the answers to waggle setup, for an install without a terminal: "--share tailscale0", for example;
#   WAGGLE_NO_TRUST  1: leave the macOS keychain alone (tests, unattended installs).
#
# Everything sits in main, called on the last line: a download cut short runs nothing.
set -eu

repo=apiar-dev/apiar-waggle

say() { printf 'waggle: %s\n' "$*"; }
fail() {
	printf 'waggle: %s\n' "$*" >&2
	exit 1
}

# git with gh's credentials only, whatever the global configuration holds: the clone is private, and the account gh is
# signed in to is the one that can read it
git_gh() { git -c credential.helper= -c 'credential.helper=!gh auth git-credential' "$@"; }

check() {
	case "$os" in
	Darwin | Linux) ;;
	*) fail "$os is not supported: waggle runs on macOS and Linux" ;;
	esac
	# On a Mac without the command line tools, git is a stub that offers to install them
	git --version >/dev/null 2>&1 || {
		[ "$os" = Darwin ] && fail "git is missing: run xcode-select --install, then run this again"
		fail "git is missing: install it with the system's package manager"
	}
	command -v gh >/dev/null 2>&1 || fail "gh is missing: https://cli.github.com, then gh auth login"
	gh auth status >/dev/null 2>&1 || fail "gh is not signed in: run gh auth login, with an account that can read $repo"
	command -v docker >/dev/null 2>&1 || fail "docker is missing: Docker Desktop on a Mac, Docker Engine on Linux"
	docker info >/dev/null 2>&1 || {
		[ "$os" = Darwin ] && fail "Docker does not answer: start Docker Desktop"
		fail "Docker does not answer: is the daemon running, and is $(id -un) in the docker group?"
	}
}

# The clone, at the wanted version. Each machine builds waggle's image from it.
fetch() {
	if [ -d "$home/.git" ]; then
		git -C "$home" diff --quiet HEAD -- || fail "$home has local changes: commit or discard them, then run this again"
		before=$(git -C "$home" rev-parse --short HEAD)
		git_gh -C "$home" fetch --quiet origin "$ref"
		git -C "$home" checkout --quiet --detach FETCH_HEAD
		after=$(git -C "$home" rev-parse --short HEAD)
		if [ "$before" = "$after" ]; then
			say "$home is up to date ($ref, $after)"
		else
			say "$home upgraded to $ref, $before → $after"
		fi
	elif [ -e "$home" ]; then
		fail "$home exists and is not a clone of waggle: move it away, or set WAGGLE_HOME"
	else
		say "cloning $repo ($ref) into $home"
		mkdir -p "$(dirname -- "$home")"
		git_gh clone --quiet --branch "$ref" "https://github.com/$repo.git" "$home"
	fi
}

# The command, linked into a directory of the PATH. A file that is not a link stays: it is not ours to replace.
link() {
	mkdir -p "$bin"
	if [ -e "$bin/waggle" ] && [ ! -L "$bin/waggle" ]; then
		fail "$bin/waggle exists and is not a link: move it away, or set WAGGLE_BIN"
	fi
	ln -sfn "$home/bin/waggle" "$bin/waggle"
}

# The line that puts $bin in the PATH, for the user's shell. The installer prints it: the shell's files are the user's.
path_hint() {
	case ":$PATH:" in
	*":$bin:"*) return 0 ;;
	esac
	case "$(basename -- "${SHELL:-sh}")" in
	zsh) line="echo 'export PATH=\"$bin:\$PATH\"' >> ~/.zshrc" ;;
	bash)
		profile=~/.bashrc
		[ "$os" = Darwin ] && profile=~/.bash_profile
		line="echo 'export PATH=\"$bin:\$PATH\"' >> $profile"
		;;
	fish) line="fish_add_path $bin" ;;
	*) line="echo 'export PATH=\"$bin:\$PATH\"' >> ~/.profile" ;;
	esac
	say "$bin is not in your PATH. Add it, then open a new terminal:"
	printf '\n  %s\n\n' "$line"
}

main() {
	os=$(uname -s)
	ref=${WAGGLE_REF:-main}
	home=${WAGGLE_HOME:-${XDG_DATA_HOME:-$HOME/.local/share}/waggle}
	bin=${WAGGLE_BIN:-$HOME/.local/bin}
	config=${WAGGLE_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/waggle/config.yaml}

	check
	fetch
	link

	if [ -f "$config" ]; then
		say "starting the instances; the first build of waggle's image takes a minute or two"
		"$home/bin/waggle" up </dev/null
		if [ "$os" = Darwin ] && [ "${WAGGLE_NO_TRUST:-}" != 1 ]; then
			"$home/bin/waggle" trust
		fi
	elif [ -n "${WAGGLE_SETUP:-}" ]; then
		# shellcheck disable=SC2086 # the answers split into options
		"$home/bin/waggle" setup $WAGGLE_SETUP </dev/null
	elif (: </dev/tty) 2>/dev/null; then
		# This script comes through a pipe: the answers come from the terminal
		say "no configuration yet: waggle setup asks it; the first build of waggle's image takes a minute or two"
		"$home/bin/waggle" setup </dev/tty
	elif [ "$os" = Darwin ]; then
		# Without a terminal, a Mac's answer is harmless: a workstation, without relay
		"$home/bin/waggle" setup </dev/null
	else
		say "no configuration yet, and no terminal to ask it: give waggle setup its answers, for example"
		printf '\n  %s setup --share none   # this machine serves its own agents only\n' "$bin/waggle"
		printf '  %s setup --share wg0    # other devices reach it through wg0\n\n' "$bin/waggle"
		say "or run this again with WAGGLE_SETUP=\"--share wg0\"."
		path_hint
		return 0
	fi
	path_hint
	say "done. Run this again to upgrade."
}

main "$@"
