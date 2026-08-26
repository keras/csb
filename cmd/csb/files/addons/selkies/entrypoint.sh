export DISPLAY=:1

# Resolve the home of the user this shell will end up as. This entrypoint is
# sourced as root BEFORE gosu drops to the mapped user, so $HOME here is
# root's — decide from HOST_UID (the eventual user) and read that user's home
# from passwd instead. Both settings below live under it.
if [ "${HOST_UID:-0}" = "0" ]; then
    _csb_home="$HOME"
else
    _csb_home=$(getent passwd "${HOST_UID}" | cut -d: -f6)
    _csb_home="${_csb_home:-$HOME}"
fi

# Point PulseAudio clients at the selkies audio daemon's anonymous socket
# so ANY app you run in the container plays into the csb-audio sink that
# selkies captures and streams to the browser. Without this, apps have no
# PULSE_SERVER, play to nowhere, and you hear silence (the audio stream
# shows 0 kbps). The socket path must match selkies-start's uid branch.
if [ "${HOST_UID:-0}" = "0" ]; then
    export PULSE_SERVER="unix:/run/selkies-audio/native"
else
    export PULSE_SERVER="unix:${_csb_home}/.selkies/pulse/native"
fi

# Join the same D-Bus session bus selkies-start runs the XFCE desktop on, so
# apps launched from this shell inherit the desktop's theme, DPI and
# notifications instead of spawning a private bus that nothing else can see.
# selkies-start binds this exact path (its $HOME/.selkies/dbus.sock).
export DBUS_SESSION_BUS_ADDRESS="unix:path=${_csb_home}/.selkies/dbus.sock"

unset _csb_home
