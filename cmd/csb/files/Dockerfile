FROM debian:stable

RUN apt-get update && apt-get install -y \
    bash-completion gosu libnss-wrapper tmux tzdata \
    && rm -rf /var/lib/apt/lists/*

# Shell setup
RUN printf '\n[ -f /usr/share/bash-completion/bash_completion ] && . /usr/share/bash-completion/bash_completion\n' \
    >> /etc/bash.bashrc

RUN mkdir -p /etc/csb/entrypoint.d /etc/csb/help.d

COPY csb/addon.d /tmp/addon.d
RUN /tmp/addon.d/run.sh

ENV LANG=C.UTF-8 LC_ALL=C.UTF-8 CSB_HOME=/home/sandbox

RUN mkdir -p $CSB_HOME /workspace && chmod 777 /workspace

COPY csb/csb-persist /usr/local/bin/csb-persist
RUN chmod +x /usr/local/bin/csb-persist

COPY csb/csb-help /usr/local/bin/csb-help
RUN chmod +x /usr/local/bin/csb-help

# MOTD: print the orientation box on interactive login shells only, so it
# never pollutes "csb -- cmd" output. Disable with CSB_MOTD=0.
# A marker in /tmp suppresses repeat display within the same container session.
# When the login session is about to auto-start tmux (CSB_TMUX=1 and not yet
# inside tmux), defer: printing here would be wiped by tmux's redraw and only
# reappear after detaching. The same snippet runs again inside the tmux window
# (TMUX set) and prints there. Deferring also skips the marker, so it still shows.
RUN printf '%s\n' \
    'case $- in *i*) ;; *) return 0 ;; esac' \
    '[ -t 1 ] || return 0' \
    '[ "${CSB_MOTD:-1}" = 0 ] && return 0' \
    '[ "${CSB_TMUX:-0}" = 1 ] && [ -z "$TMUX" ] && return 0' \
    '[ -f /tmp/.csb-motd-shown ] && return 0' \
    'touch /tmp/.csb-motd-shown' \
    'command -v csb-help >/dev/null 2>&1 && csb-help' \
    > /etc/profile.d/csb-motd.sh

# PATH: the entrypoint prepends ~/.local/bin and ~/bin to PATH, which the gosu
# path inherits. login(1) (used by the systemd addon's interactive shell) instead
# regenerates PATH from /etc/login.defs and drops that prefix, so re-add it for
# login shells. Idempotent — the default 'bash -l' already carries the prefix, so
# the guard makes it a no-op there.
RUN printf '%s\n' \
    'case ":${PATH}:" in' \
    '    *":${HOME}/.local/bin:"*) ;;' \
    '    *) PATH="${HOME}/.local/bin:${HOME}/bin:${PATH}"; export PATH ;;' \
    'esac' \
    > /etc/profile.d/csb-path.sh

# tmux auto-start for the interactive login session. Under the systemd addon the
# interactive entry is a logind session via login(1), which runs a login shell —
# not the tmux command csb otherwise uses as the container CMD. When tmux is
# requested (CSB_TMUX=1) start it here so the tmux server runs inside the logind
# session. Guarded: interactive login shell on a tty, and never recurse when
# already inside tmux (so the default non-systemd path's tmux windows skip it).
RUN printf '%s\n' \
    'case $- in *i*) ;; *) return 0 ;; esac' \
    '[ -t 1 ] || return 0' \
    '[ "${CSB_TMUX:-0}" = 1 ] || return 0' \
    '[ -n "$TMUX" ] && return 0' \
    'command -v tmux >/dev/null 2>&1 || return 0' \
    'exec tmux new-session -A -s main' \
    > /etc/profile.d/csb-tmux.sh

COPY csb/csb-host-run /usr/local/bin/csb-host-run

# Entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
CMD ["bash", "-l"]
