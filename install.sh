#!/bin/sh
# Builds the myDiary desktop app and installs it with a menu entry.
#
#   ./install.sh                     install for the current user (~/.local)
#   ./install.sh --system            install for all users (/usr/local, uses sudo)
#   ./install.sh --uninstall         remove the user install
#   ./install.sh --uninstall --system
#
# Building needs Go and the GTK 4 development files.
set -eu

# Must match AppID in internal/ui/app.go so the desktop links the window to the entry.
app_id=io.github.oupsman.MyDiary

system=false
uninstall=false
for arg in "$@"; do
	case $arg in
	--system) system=true ;;
	--uninstall) uninstall=true ;;
	*)
		sed -n '2,9s/^# \{0,1\}//p' "$0" >&2
		exit 2
		;;
	esac
done

if $system; then
	bindir=/usr/local/bin
	appdir=/usr/local/share/applications
	sudo=$([ "$(id -u)" -eq 0 ] || echo sudo)
else
	bindir=${XDG_BIN_HOME:-$HOME/.local/bin}
	appdir=${XDG_DATA_HOME:-$HOME/.local/share}/applications
	sudo=
fi
desktop=$appdir/$app_id.desktop

refresh_menu() {
	if command -v update-desktop-database >/dev/null; then
		$sudo update-desktop-database -q "$appdir" || true
	fi
}

if $uninstall; then
	$sudo rm -f "$bindir/mydiary" "$desktop"
	refresh_menu
	echo "Removed $bindir/mydiary and $desktop"
	exit 0
fi

# Build as the invoking user, even for --system: root often has no Go toolchain.
cd "$(dirname "$0")"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "Building mydiary..."
go build -trimpath -o "$tmp/mydiary" .

cat >"$tmp/$app_id.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=myDiary
GenericName=Diary
Comment=Private, encrypted diary
Exec=$bindir/mydiary
Icon=accessories-text-editor
Terminal=false
Categories=Office;GTK;
Keywords=diary;journal;notes;encrypted;
StartupNotify=true
EOF

$sudo install -d "$bindir" "$appdir"
$sudo install -m 0755 "$tmp/mydiary" "$bindir/mydiary"
$sudo install -m 0644 "$tmp/$app_id.desktop" "$desktop"
refresh_menu

echo "Installed $bindir/mydiary and $desktop"
