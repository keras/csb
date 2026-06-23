export DISPLAY=:1
# Point PulseAudio clients at the selkies audio daemon's anonymous socket
# so ANY app you run in the container plays into the csb-audio sink that
# selkies captures and streams to the browser. Without this, apps have no
# PULSE_SERVER, play to nowhere, and you hear silence (the audio stream
# shows 0 kbps). The socket path must match selkies-start's uid branch.
# NOTE: this entrypoint is sourced as root BEFORE gosu drops to the mapped
# user, so decide from HOST_UID (the eventual user), not $(id -u), and
# resolve that user's home from passwd — using $HOME here would give
# root's home and point at the wrong (non-existent) socket.
if [ "${HOST_UID:-0}" = "0" ]; then
    export PULSE_SERVER="unix:/run/selkies-audio/native"
else
    _csb_home=$(getent passwd "${HOST_UID}" | cut -d: -f6)
    export PULSE_SERVER="unix:${_csb_home:-$HOME}/.selkies/pulse/native"
    unset _csb_home
fi
