#!/usr/bin/env sh
# manage-skills.sh <link|unlink> PKG_DIR SKILLS_DIR [MANAGED_ROOT...]
#
# Symlinks (link) or removes (unlink) the relay-generated skills under
# PKG_DIR/skills into SKILLS_DIR, without disturbing skills relay does not own.
#
# Ownership rules:
#   * A pre-existing target that is NOT a symlink (a real file/dir) is left
#     untouched and skipped.
#   * A symlink whose target points under one of the MANAGED_ROOT paths is
#     considered relay-managed and is replaced/removed silently.
#   * A symlink whose target is NOT under a managed root is flagged; on `link`
#     the user is asked whether to replace it (defaults to "no" when stdin is
#     not a terminal), and on `unlink` it is always kept.
set -eu

mode=${1:-}
pkg_dir=${2:-}
skills_dir=${3:-}
if [ "$#" -lt 3 ]; then
	echo "usage: manage-skills.sh <link|unlink> PKG_DIR SKILLS_DIR [MANAGED_ROOT...]" >&2
	exit 2
fi
shift 3
# Capture the relay-managed roots into a space-separated list. Paths are not
# expected to contain spaces (they are $HOME and the repo root).
managed_roots=$*

# is_managed TARGET — succeeds when TARGET lies under any managed root.
# A target containing a ".." segment is never treated as managed: a textual
# prefix match ("$root"/*) could otherwise be fooled by "$root/../elsewhere",
# which resolves outside the managed root.
is_managed() {
	_t=$1
	case "$_t" in
	*/../* | ../* | */.. | ..) return 1 ;;
	esac
	for _root in $managed_roots; do
		case "$_t" in
		"$_root"/* | "$_root") return 0 ;;
		esac
	done
	return 1
}

case "$mode" in
link)
	mkdir -p "$skills_dir"
	;;
unlink) ;;
*)
	echo "manage-skills.sh: unknown mode '$mode' (want link or unlink)" >&2
	exit 2
	;;
esac

for d in "$pkg_dir"/skills/*/; do
	[ -d "$d" ] || continue
	name=$(basename "$d")
	target="$skills_dir/$name"

	if [ -L "$target" ]; then
		cur=$(readlink "$target")
		if ! is_managed "$cur"; then
			if [ "$mode" = "unlink" ]; then
				echo "  keeping $name: $target -> $cur is not managed by relay"
				continue
			fi
			echo "  $target -> $cur is not managed by relay"
			ans=n
			if [ -t 0 ]; then
				printf '  Replace it with the relay skill "%s"? [y/N] ' "$name"
				read ans || ans=n
			else
				echo "  (non-interactive: keeping existing symlink)"
			fi
			case "$ans" in
			y | Y | yes | Yes) ;;
			*)
				echo "  skipping $name"
				continue
				;;
			esac
		fi
	elif [ -e "$target" ]; then
		echo "  skipping $name: $target exists and is not a symlink"
		continue
	fi

	if [ "$mode" = "unlink" ]; then
		rm -f "$target"
		echo "  removed $name"
	else
		rm -f "$target"
		ln -sf "$d" "$target"
		echo "  linked $name"
	fi
done
